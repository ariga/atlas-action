// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"ariga.io/atlas-action/internal/github"
	"ariga.io/atlas/atlasexec"
	"ariga.io/atlas/sql/migrate"
)

const (
	// sumMergeDriver is the name of the merge driver that resolves the conflicts in
	// atlas.sum while the commits of a layer are replayed. The file is regenerated
	// after the replay, so the driver keeps whichever version is checked out.
	sumMergeDriver = "atlas-sum"
	// maxClockWait is the longest the action waits for the clock to pass a migration
	// version, before it moves the migrations above it.
	maxClockWait = 5 * time.Minute
	// versionLayout is the layout of the versions `atlas migrate rebase` writes.
	versionLayout = "20060102150405"
	// stackHint is appended to the errors that leave a stack untouched.
	stackHint = "Nothing was pushed. Restack it locally with `gh stack rebase`, then push it with `gh stack push`"
)

type (
	// restacker restacks stacked pull requests whose migration directories conflict.
	restacker struct {
		*Actions
		tc      *TriggerContext
		sc      StackClient
		remote  string
		dirURL  string
		dir     string // Migration directory, relative to the working directory.
		rel     string // Migration directory, relative to the repository root.
		atlas   bool   // Whether the directory has the default Atlas format.
		message string // Message of the commits that fix the migration directory.
		now     func() time.Time
		sleep   func(context.Context, time.Duration) error
		// moved holds the heads the branches had before this run restacked them,
		// for the stacks that are based on them.
		moved map[string]string
		// hasPR caches whether each base branch has an open pull request.
		hasPR map[string]bool
		// history caches the heads each branch had, for the stacks that share the
		// branch, like the trunk of several stacks. It is nil if unknown.
		history map[string]*BranchHistory
		// rewrites caches the heads each trunk without a pull request had around its force-pushes.
		rewrites map[string][]string
	}
	// layerState is the state of a layer while its stack is restacked.
	layerState struct {
		StackLayer
		old, new string // Head commit before and after the restack.
		fork     *fork  // Where the own commits of the layer start.
	}
	// fork is the commit a layer was built on: the newest commit of the
	// layer below it, or of the trunk, that the layer has.
	fork struct {
		commit   string // The fork point.
		upstream string // The upstream to replay the commits after the fork point from.
		reason   string // Why the fork point is not trusted, empty if it is known to be exact.
		// others holds the heads of the branch below that the layer also has, which neither contain the fork point nor
		// each other, like after the layer merged an old copy of that branch. The replay skips their commits too.
		others []string
	}
)

// migrateAutoRebaseStacks restacks the stack of the branch the action runs on, and the stacks
// that target the branch. A pull request of the branch that is not stacked and targets the base
// branch is rebased onto it first, as without the "stack" input.
func (a *Actions) migrateAutoRebaseStacks(ctx context.Context, tc *TriggerContext) error {
	if t := a.GetType(); t != atlasexec.TriggerTypeGithubAction {
		return fmt.Errorf(`the "stack" input is supported only on GitHub Actions, not on %s`, t)
	}
	if strings.TrimSpace(a.GetInput("base-sha")) != "" || a.GetBoolInput("force-rebase") {
		return errors.New(`the "stack" input cannot be used with "base-sha" or "force-rebase"`)
	}
	var sc StackClient
	if tc.SCMClient != nil {
		c, err := tc.SCMClient()
		if err != nil {
			return fmt.Errorf("failed to create the SCM client: %w", err)
		}
		sc, _ = c.(StackClient)
	}
	if sc == nil {
		return errors.New(`the "stack" input is supported only for repositories hosted on GitHub`)
	}
	if tc.Branch == "" {
		return errors.New("failed to determine the branch the action runs on")
	}
	stacks, single, lookupErr, err := a.branchStacks(ctx, sc, tc)
	if err != nil {
		return err
	}
	var branchErr error
	if single {
		if branchErr = a.migrateAutoRebaseBranch(ctx, tc); branchErr != nil && len(stacks) > 0 {
			a.Warningf("failed to rebase %q, restacking the stacks that target it: %v", tc.Branch, branchErr)
		}
	}
	if len(stacks) == 0 {
		if !single {
			a.SetOutput("rebased", "false")
		}
		return errors.Join(branchErr, lookupErr)
	}
	r, err := a.newRestacker(ctx, tc, sc)
	if err != nil {
		return errors.Join(branchErr, lookupErr, err)
	}
	if branchErr != nil {
		// Leave the merge that the failed rebase may have left in progress.
		if err := r.discard(ctx); err != nil {
			return errors.Join(branchErr, lookupErr, err)
		}
	} else if single && tc.Commit != "" {
		// The rebase of the branch moves it like a restack, and the stacks on it are built on the head it had.
		if head, err := r.git(ctx, "rev-parse", "--verify", "--quiet", r.remoteRef(tc.Branch)); err == nil && head != tc.Commit {
			r.moved[tc.Branch] = tc.Commit
		}
	}
	pushed, current, err := r.run(ctx, stacks)
	// Keep the output of the rebase of the branch, unless the stacks were pushed.
	if pushed || !single || branchErr != nil {
		a.SetOutput("rebased", strconv.FormatBool(pushed))
	}
	if current != "" {
		if latest, err := a.Atlas.MigrateLs(ctx, &atlasexec.MigrateLsParams{
			DirURL: r.dirURL,
			Short:  true,
			Latest: true,
		}); err != nil {
			a.Warningf("failed to get latest migration version: %v", err)
		} else {
			a.SetOutput("latest_version", strings.TrimSpace(latest))
		}
	}
	return errors.Join(branchErr, lookupErr, err)
}

// branchStacks returns the stacks to restack for the branch: its own stack, if one of its open pull
// requests is stacked, and the open stacks that target the branch. single reports whether the branch
// has an open pull request that is not stacked and targets the base branch, which is rebased onto it
// as without the "stack" input. Failed lookups of stacks are returned in lookupErr, and do not stop
// the others.
func (a *Actions) branchStacks(ctx context.Context, sc StackClient, tc *TriggerContext) (stacks []*Stack, single bool, lookupErr, _ error) {
	prs, err := sc.OpenPullRequests(ctx, tc.Branch, "")
	if err != nil {
		return nil, false, nil, fmt.Errorf("failed to list the open pull requests of %q: %w", tc.Branch, err)
	}
	var nums []int
	for _, pr := range prs {
		if pr.Stack != 0 && !slices.Contains(nums, pr.Stack) {
			nums = append(nums, pr.Stack)
		}
	}
	if len(nums) > 1 {
		slices.Sort(nums)
		return nil, false, nil, fmt.Errorf("branch %q belongs to more than one stack (%s), restack them manually", tc.Branch, stackNumbers(nums))
	}
	var errs []error
	if len(nums) == 1 {
		switch s, err := sc.Stack(ctx, nums[0]); {
		case err != nil:
			errs = append(errs, fmt.Errorf("failed to get stack #%d: %w", nums[0], err))
		case s == nil:
			// The pull request was unstacked after it was listed.
			a.Warningf("Stack #%d of %q was not found, skipping it", nums[0], tc.Branch)
			nums = nil
		default:
			stacks = append(stacks, s)
		}
	}
	if len(nums) == 0 && len(prs) > 0 {
		switch base := a.GetInputDefault("base-branch", tc.DefaultBranch); {
		case base == "":
			a.Warningf(`Pull request #%d of %q is not stacked, set the "base-branch" input to rebase it`, prs[0].Number, tc.Branch)
		case slices.ContainsFunc(prs, func(pr *PullRequest) bool { return pr.Base == base }):
			single = true
		default:
			a.Infof("Pull request #%d of %q targets %q and is not stacked", prs[0].Number, tc.Branch, prs[0].Base)
		}
	}
	// The branch may be the trunk of stacks, like main, a long-lived branch, or a branch of a stack.
	seen := make(map[int]bool)
	for _, s := range stacks {
		seen[s.Number] = true
	}
	more, err := a.targetStacks(ctx, sc, tc.Branch, seen)
	if _, ok := errors.AsType[*listError](err); ok {
		return nil, false, nil, err
	}
	stacks = append(stacks, more...)
	if len(stacks) == 0 && !single && err == nil && len(errs) == 0 {
		a.Infof("No stacks target %q, nothing to restack", tc.Branch)
	}
	return stacks, single, errors.Join(append(errs, err)...), nil
}

// listError is returned when the pull requests that target a branch cannot be listed.
type listError struct{ error }

func (e *listError) Unwrap() error { return e.error }

// targetStacks returns the stacks that target the branch and are not seen yet, sorted by their
// numbers, and marks them as seen. It skips the stacks that are not found, unless none of them is.
func (a *Actions) targetStacks(ctx context.Context, sc StackClient, branch string, seen map[int]bool) ([]*Stack, error) {
	prs, err := sc.OpenPullRequests(ctx, "", branch)
	if err != nil {
		return nil, &listError{fmt.Errorf("failed to list the open pull requests that target %q: %w", branch, err)}
	}
	var (
		stacks  []*Stack
		errs    []error
		missing []int
		lookups int
	)
	for _, pr := range prs {
		if pr.Stack == 0 || seen[pr.Stack] {
			continue
		}
		seen[pr.Stack] = true
		lookups++
		switch s, err := sc.Stack(ctx, pr.Stack); {
		case err != nil:
			errs = append(errs, fmt.Errorf("failed to get stack #%d: %w", pr.Stack, err))
		case s == nil:
			// The pull request was unstacked after it was listed.
			a.Warningf("Stack #%d of pull request #%d was not found, skipping it", pr.Stack, pr.Number)
			missing = append(missing, pr.Stack)
		case s.Base == branch:
			stacks = append(stacks, s)
		}
	}
	if len(missing) > 0 && len(missing) == lookups {
		slices.Sort(missing)
		errs = append(errs, fmt.Errorf("none of the stacks of the pull requests that target %q was found (%s)", branch, stackNumbers(missing)))
	}
	slices.SortFunc(stacks, func(x, y *Stack) int { return cmp.Compare(x.Number, y.Number) })
	return stacks, errors.Join(errs...)
}

func (a *Actions) newRestacker(ctx context.Context, tc *TriggerContext, sc StackClient) (*restacker, error) {
	u, err := url.Parse(a.GetInputDefault("dir", "file://migrations"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse dir URL: %w", err)
	}
	if u.Scheme != "file" {
		return nil, fmt.Errorf(`the "stack" input requires a local migration directory (file://), got %q`, u.Redacted())
	}
	r := &restacker{
		Actions:  a,
		tc:       tc,
		sc:       sc,
		remote:   a.GetInputDefault("remote", "origin"),
		dirURL:   u.String(),
		dir:      filepath.Join(u.Host, u.Path),
		atlas:    u.Query().Get("format") == "" || u.Query().Get("format") == formatAtlas,
		moved:    make(map[string]string),
		hasPR:    make(map[string]bool),
		history:  make(map[string]*BranchHistory),
		rewrites: make(map[string][]string),
		now:      time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		},
	}
	r.message = fmt.Sprintf("%s: rebase migration files", r.dir)
	v, err := r.git(ctx, "--version")
	if err != nil {
		return nil, err
	}
	a.Infof("auto-rebase stacks with %s", v)
	switch out, err := r.git(ctx, "rev-parse", "--is-shallow-repository"); {
	case err != nil:
		return nil, err
	case out == "true":
		return nil, errors.New("restacking requires the full git history, set `fetch-depth: 0` in actions/checkout")
	}
	top, err := r.git(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	if r.rel, err = repoPath(top, r.dir); err != nil {
		return nil, err
	}
	return r, nil
}

const formatAtlas = "atlas"

// repoPath returns the path of the migration directory relative to the repository root.
func repoPath(top, dir string) (string, error) {
	abs := dir
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		// Git reports the root with symbolic links resolved.
		if c, err := filepath.EvalSymlinks(cwd); err == nil {
			cwd = c
		}
		abs = filepath.Join(cwd, dir)
		// The directory is hashed in the working tree, but git does not follow symbolic links.
		if resolved, err := filepath.EvalSymlinks(abs); err == nil && resolved != abs {
			return "", fmt.Errorf("restacking does not support a migration directory that is a symbolic link, or under one: %q", dir)
		}
	} else if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if t, err := filepath.EvalSymlinks(top); err == nil {
		top = t
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("migration directory %q is outside of the git repository", dir)
	}
	if strings.ContainsAny(rel, " \t\r\n\"'\\*?[]!#:") {
		return "", fmt.Errorf("restacking does not support migration directories with spaces or special characters in their path: %q", rel)
	}
	return filepath.ToSlash(rel), nil
}

// run restacks the given stacks, and reports whether it pushed any branch, and the new head
// of the branch the action runs on, if it was restacked. Each stack is pushed on its own, all
// its branches at once, so a failure leaves its branches as they were and does not stop the
// other stacks.
func (r *restacker) run(ctx context.Context, stacks []*Stack) (pushed bool, current string, err error) {
	// Restore the branch, or the detached commit, the action started on.
	start := []string{"switch", "--quiet", "--discard-changes"}
	if ref, err := r.git(ctx, "symbolic-ref", "--quiet", "HEAD"); err == nil && strings.HasPrefix(ref, "refs/heads/") {
		start = append(start, strings.TrimPrefix(ref, "refs/heads/"))
	} else if sha, err := r.git(ctx, "rev-parse", "HEAD"); err == nil {
		start = append(start, "--detach", sha)
	} else {
		return false, "", err
	}
	switch out, err := r.git(ctx, "status", "--porcelain", "--untracked-files=no"); {
	case err != nil:
		return false, "", err
	case out != "":
		return false, "", fmt.Errorf("restacking requires a clean working tree, found changes in:\n%s", out)
	}
	// Migration files that git does not track are hashed with the directory.
	switch out, err := r.git(ctx, "status", "--porcelain", "--untracked-files=all", "--", r.sqlSpec()); {
	case err != nil:
		return false, "", err
	case out != "":
		return false, "", fmt.Errorf("restacking requires a clean migration directory, found changes in:\n%s", out)
	}
	restore, err := r.installMergeDriver(ctx)
	if err != nil {
		return false, "", err
	}
	defer func() {
		rerr := restore()
		args := start
		if current != "" {
			args = []string{"switch", "--quiet", "--discard-changes", "--force-create", r.tc.Branch, current}
		}
		_, cerr := r.git(ctx, args...)
		err = errors.Join(err, rerr, cerr, r.clean(ctx))
	}()
	var errs []error
	seen := make(map[int]bool)
	for _, s := range stacks {
		seen[s.Number] = true
	}
	// The list grows with the stacks based on the branches that are restacked.
	for i := 0; i < len(stacks); i++ {
		s := stacks[i]
		layers, err := r.restack(ctx, s)
		if err != nil {
			errs = append(errs, fmt.Errorf("stack #%d: %w", s.Number, err))
			continue
		}
		for _, l := range layers {
			if l.old == l.new {
				continue
			}
			pushed = true
			if l.Branch == r.tc.Branch {
				current = l.new
			}
			r.moved[l.Branch] = l.old
			// Stacks based on the branch, like a stack based on a branch of another stack.
			more, err := r.targetStacks(ctx, r.sc, l.Branch, seen)
			if err != nil {
				errs = append(errs, err)
			}
			stacks = append(stacks, more...)
		}
	}
	return pushed, current, errors.Join(errs...)
}

// restack restacks the open layers of the stack whose migration directories
// conflict, and the layers above them. All the restacked branches are pushed
// at once, and nothing is pushed if one of them fails.
func (r *restacker) restack(ctx context.Context, s *Stack) ([]*layerState, error) {
	var (
		merged *StackLayer // The top merged layer, if any.
		layers []*layerState
	)
open:
	for i, l := range s.Layers {
		switch {
		case l.Merged:
			merged = &s.Layers[i]
		case l.Open:
			layers = append(layers, &layerState{StackLayer: l})
		default:
			// Pull requests above a closed one cannot merge, until it is reopened or unstacked.
			if above := len(s.Layers) - i - 1; above > 0 {
				r.Warningf("Stack #%d: pull request #%d is closed, skipping the %d pull request(s) above it", s.Number, l.Number, above)
			}
			break open
		}
	}
	if len(layers) == 0 {
		r.Infof("Stack #%d has no open pull requests, nothing to restack", s.Number)
		return nil, nil
	}
	// The layers are rebased in the order GitHub lists them, from the bottom up.
	for i, l := range layers[1:] {
		if below := layers[i]; l.Base != below.Branch {
			return nil, fmt.Errorf("pull request #%d targets %q, not the branch of #%d (%s) listed below it", l.Number, l.Base, below.Number, below.Branch)
		}
	}
	// The bottom one targets the base of the stack, or, until GitHub retargets it, the branch that merged below it.
	if l := layers[0]; l.Base != s.Base && (merged == nil || l.Base != merged.Branch) {
		return nil, fmt.Errorf("pull request #%d targets %q, not the base of the stack %q", l.Number, l.Base, s.Base)
	}
	r.Infof("Stack #%d: %s", s.Number, describeStack(s.Base, layers))
	if err := r.fetch(ctx, s.Base, merged, layers); err != nil {
		return nil, err
	}
	trunk, err := r.git(ctx, "rev-parse", "--verify", "--quiet", r.remoteRef(s.Base)+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the base branch %q: %w", s.Base, err)
	}
	// Most runs find nothing to restack, and need neither the history of the branches nor the fork points.
	switch ok, err := r.allOnTop(ctx, trunk, layers); {
	case err != nil:
		return nil, err
	case ok:
		r.Infof("Migrations of all the pull requests are up to date, nothing to restack")
		return layers, nil
	}
	// The branches below the layers.
	var branches []string
	if merged != nil {
		branches = append(branches, merged.Branch)
	}
	for _, l := range layers[:len(layers)-1] {
		branches = append(branches, l.Branch)
	}
	switch ok, err := r.baseHasPR(ctx, s.Base); {
	case err != nil:
		return nil, err
	case ok:
		branches = append(branches, s.Base)
	}
	history := r.histories(ctx, branches)
	for i, l := range layers {
		var below *layerState
		if i > 0 {
			below = layers[i-1]
		}
		if l.fork, err = r.forkPoint(ctx, s, l, below, merged, trunk, history); err != nil {
			return nil, err
		}
	}
	start, err := r.firstConflict(ctx, s.Base, trunk, layers)
	if err != nil || start < 0 {
		return layers, err
	}
	for i := start; i < len(layers); i++ {
		l, parent, parentName := layers[i], trunk, s.Base
		var below *layerState
		if i > 0 {
			below = layers[i-1]
			parent, parentName = below.new, fmt.Sprintf("#%d (%s)", below.Number, below.Branch)
		}
		if l.fork.reason != "" {
			return nil, fmt.Errorf("#%d (%s): %s. %s", l.Number, l.Branch, l.fork.reason, stackHint)
		}
		if l.new, err = r.restackLayer(ctx, l, parent, parentName); err != nil {
			// Discard the changes of the failed layer, before the next stack is restacked.
			err = errors.Join(err, r.discard(ctx))
			return nil, fmt.Errorf("#%d (%s): %w. %s", l.Number, l.Branch, err, stackHint)
		}
	}
	return layers, r.push(ctx, s, layers)
}

// belowName names what the layer was built on.
func belowName(base string, below *layerState, merged *StackLayer) string {
	switch {
	case below != nil:
		return fmt.Sprintf("#%d (%s)", below.Number, below.Branch)
	case merged != nil:
		return fmt.Sprintf("the merged #%d (%s)", merged.Number, merged.Branch)
	default:
		return base
	}
}

// baseHasPR reports whether the base branch has an open pull request. Such
// a base branch may be rewritten, like the branches of a stack.
func (r *restacker) baseHasPR(ctx context.Context, base string) (bool, error) {
	if ok, cached := r.hasPR[base]; cached {
		return ok, nil
	}
	prs, err := r.sc.OpenPullRequests(ctx, base, "")
	if err != nil {
		return false, fmt.Errorf("failed to list the open pull requests of the base branch %q: %w", base, err)
	}
	r.hasPR[base] = len(prs) > 0
	return len(prs) > 0, nil
}

// histories returns the heads the given branches had, as the activity of the repository records
// them. The branches whose activity cannot be read have no history.
func (r *restacker) histories(ctx context.Context, branches []string) map[string]*BranchHistory {
	history := make(map[string]*BranchHistory, len(branches))
	for _, b := range branches {
		h, ok := r.history[b]
		if !ok {
			var err error
			if h, err = r.sc.BranchHistory(ctx, b); err != nil {
				r.Warningf("failed to read the activity of branch %q: %v", b, err)
			}
			r.history[b] = h
		}
		history[b] = h
	}
	return history
}

// forkPoint returns where the own commits of the layer start: the newest merge-base of the layer
// with the heads the branch below it had. For the bottom layer, these are the trunk, the heads of
// the merged pull request below it, if any, the head the trunk had before the push that triggered
// the run, and the heads of the trunk: all of them if it has a pull request of its own, and the ones
// around its force-pushes otherwise. For the others, the head the layer below has, and the heads its
// branch had. The heads that were force-pushed away are fetched by their SHA, as a clone does not
// have them, and the fork point is not trusted if one of them cannot be fetched, except the ones of a
// trunk without a pull request, which are left out.
//
// The activity of a branch records every head it had since it was created, so with it the fork point
// is exact. An activity that does not go back that far is enough when it records no force-push, and
// goes back to a head the layer has. Otherwise, the commits the branch below rewrote cannot be told
// from the own commits of the layer, and the fork point is trusted only if it is the latest head of the
// branch below, so the layer has all of it. In both cases, commits the branch below dropped before the
// recorded activity, if any, stay in the layer. Like `gh stack rebase`, the restack does not replay the
// commits of the branch below into the layer, but it refuses when the branch below no longer has some
// of them in any form, as they may have been moved to the layer, like when a branch is split into a
// stack. Commits the branch below had only locally are the layer's own.
func (r *restacker) forkPoint(ctx context.Context, s *Stack, l, below *layerState, merged *StackLayer, trunk string, history map[string]*BranchHistory) (*fork, error) {
	var (
		f    = &fork{}
		base string   // Merge-base of the bottom layer with the trunk.
		tips []string // Heads the branches below had.
		now  []string // Heads the branches below have now.
		held []string // Latest heads of the branches below, known without their activity.
		skip string   // Head of the branch below, whose patches the replay skips.
		gaps []string // Why the activity of the branches below may miss heads the layer was built on.
		more []string // Heads of the trunk that may not be fetched.
		hist = func(branch, head string) {
			h := history[branch]
			if h != nil {
				tips = append(tips, h.Heads...)
			}
			if gap := r.activityGap(ctx, h, branch, head, l.old); gap != "" {
				gaps = append(gaps, gap)
			}
		}
	)
	switch {
	case below != nil:
		tips, skip = append(tips, below.old), below.old
		now, held = append(now, below.old), append(held, below.old)
		// The push that triggered the run may have rewritten the branch below, after the layer was built on it.
		if r.tc.Before != "" && r.tc.Branch == below.Branch && r.tc.Commit == below.old {
			tips, held = append(tips, r.tc.Before), append(held, r.tc.Before)
		}
		hist(below.Branch, below.old)
	default:
		now, held = append(now, trunk), append(held, trunk)
		if merged != nil {
			head, err := r.git(ctx, "rev-parse", "--verify", "--quiet", r.mergedRef(merged)+"^{commit}")
			if err == nil {
				tips, now, held = append(tips, head), append(now, head), append(held, head)
			}
			hist(merged.Branch, head)
		}
		// A trunk without a pull request is expected to only move forward, but the layer may
		// hold commits of the merged pull request below it, that are not in the trunk.
		mb, err := r.git(ctx, "merge-base", trunk, l.old)
		if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
			return nil, fmt.Errorf("#%d (%s): no commit in common with %s", l.Number, l.Branch, s.Base)
		}
		if err != nil {
			return nil, err
		}
		f.commit, f.upstream, base = mb, trunk, mb
		// The head the trunk had before this run restacked it, as a branch of another stack.
		start := trunk
		if old, ok := r.moved[s.Base]; ok {
			start = old
			tips, held, skip = append(tips, old), append(held, old), old
		}
		// The push that triggered the run may have rewritten the trunk, even one without a pull request.
		if r.tc.Before != "" && r.tc.Branch == s.Base && r.tc.Commit == start {
			tips, held = append(tips, r.tc.Before), append(held, r.tc.Before)
		}
		ok, err := r.baseHasPR(ctx, s.Base)
		if err != nil {
			return nil, err
		}
		if ok {
			// The trunk is the branch of a pull request, that may be rewritten.
			hist(s.Base, trunk)
		} else {
			// A trunk without a pull request is expected to only move forward, but it may have been force-pushed
			// before the push of this run, and the layer may still have commits it dropped.
			var gap string
			if more, gap = r.trunkRewrites(ctx, s.Base); gap != "" {
				gaps = append(gaps, gap)
			}
		}
	}
	fetchErr := r.fetchMissing(ctx, tips)
	tips = append(tips, more...)
	cands, err := r.forkCandidates(ctx, l.old, tips)
	if err != nil {
		return nil, err
	}
	var skipBase string // The merge-base of the layer with the head whose patches the replay skips.
	if skip != "" {
		skipBase, _ = r.git(ctx, "merge-base", skip, l.old)
	}
	for _, c := range cands {
		if c == f.commit {
			continue
		}
		if f.commit != "" {
			if newer, err := r.gitTest(ctx, "merge-base", "--is-ancestor", f.commit, c); err != nil || !newer {
				// A head of the branch below that the trunk does not have wins over the merge-base with the
				// trunk, like when the layer merged the trunk after it was built on the merged pull request.
				if f.commit != base {
					continue
				}
				if older, err := r.gitTest(ctx, "merge-base", "--is-ancestor", c, base); err != nil || older {
					continue
				}
			}
		}
		// Replaying from a head of the branch below also skips the commits it has the patches of.
		f.commit, f.upstream = c, c
		if c == skipBase {
			f.upstream = skip
		}
	}
	name := belowName(s.Base, below, merged)
	if f.commit == "" {
		return nil, fmt.Errorf("#%d (%s): no commit in common with %s", l.Number, l.Branch, name)
	}
	if fetchErr != nil {
		f.reason = fmt.Sprintf("cannot fetch the heads %s had: %v", name, fetchErr)
		return f, nil
	}
	// The layer may also have heads of the branch below that do not contain the fork point, like after it merged an
	// old copy of that branch. Their commits are not the layer's either, so they are checked like the ones up to it.
	if len(cands) > 1 {
		out, err := r.git(ctx, append([]string{"merge-base", "--independent", f.commit}, cands...)...)
		if err != nil {
			return nil, err
		}
		for _, h := range strings.Fields(out) {
			if h == f.commit {
				continue
			}
			// The commits of the trunk are never replayed, nor lost.
			if inTrunk, err := r.gitTest(ctx, "merge-base", "--is-ancestor", h, trunk); err != nil {
				return nil, err
			} else if !inTrunk {
				f.others = append(f.others, h)
			}
		}
	}
	// The branch below dropped these commits, or they were moved to the layer. Git cannot tell which.
	var dropped []string
	for _, h := range append([]string{f.commit}, f.others...) {
		d, err := r.dropped(ctx, h, now, trunk)
		if err != nil {
			return nil, err
		}
		for _, c := range d {
			if !slices.Contains(dropped, c) {
				dropped = append(dropped, c)
			}
		}
	}
	if len(dropped) > 0 {
		f.reason = fmt.Sprintf("it has commits that %s no longer has, which a restack would drop: %s", name, strings.Join(dropped, ", "))
		return f, nil
	}
	if len(gaps) > 0 {
		gap := strings.Join(gaps, "; ")
		if !slices.Contains(held, f.commit) {
			f.reason = fmt.Sprintf("%s. It is not built on the latest head of %s, so its commits cannot be told from the ones %s had", gap, name, name)
			return f, nil
		}
		r.Warningf("#%d (%s): %s. It is built on the latest head of %s, so its own commits start there, and the commits %s dropped before, if any, stay in it",
			l.Number, l.Branch, gap, name, name)
	}
	return f, nil
}

// trunkRewrites returns the heads the trunk had around its force-pushes, and fetches the ones the clone does not
// have. As a trunk without a pull request is expected to only move forward, heads that cannot be fetched are left
// out, and so are all of them if the activity is not available (404). If reading the activity fails otherwise,
// it returns why, as a gap in the history of the trunk.
func (r *restacker) trunkRewrites(ctx context.Context, branch string) (heads []string, gap string) {
	if heads, ok := r.rewrites[branch]; ok {
		return heads, ""
	}
	heads, err := r.sc.ForcePushes(ctx, branch)
	switch {
	case err != nil:
		r.Warningf("failed to read the force-pushes of branch %q: %v", branch, err)
		if !errors.Is(err, github.ErrNotFound) {
			return nil, fmt.Sprintf("the force-pushes of branch %q cannot be read", branch)
		}
	default:
		if err := r.fetchMissing(ctx, heads); err != nil {
			r.Warningf("failed to fetch some of the heads branch %q was force-pushed from: %v", branch, err)
		}
	}
	r.rewrites[branch] = heads
	return heads, ""
}

// forkCandidates returns the newest commits of the layer that one of the heads has, in a single walk of the
// history, or the head of the layer if one of the heads has all of it. Heads the repository does not have,
// like the ones that cannot be fetched, and heads with a history unrelated to the layer's, are ignored.
func (r *restacker) forkCandidates(ctx context.Context, layer string, heads []string) ([]string, error) {
	if len(heads) == 0 {
		return nil, nil
	}
	var in strings.Builder
	for _, h := range heads {
		// "--not" does not apply to the revisions read from the standard input.
		fmt.Fprintf(&in, "^%s\n", h)
	}
	out, err := r.gitInput(ctx, strings.NewReader(in.String()), "rev-list", "--boundary", "--ignore-missing", layer, "--stdin")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return []string{layer}, nil
	}
	var cands []string
	// The boundary commits are the ones the heads have, whose children only the layer has.
	for l := range strings.SplitSeq(string(out), "\n") {
		if c, ok := strings.CutPrefix(l, "-"); ok {
			cands = append(cands, c)
		}
	}
	return cands, nil
}

// activityGap returns why the activity of a branch below the layer may miss the heads the layer was built
// on, or an empty string if it has them all: it goes back to the creation of the branch, or the branch only
// moved forward since a head the layer has.
func (r *restacker) activityGap(ctx context.Context, h *BranchHistory, branch, head, layer string) string {
	switch {
	case h == nil:
		return fmt.Sprintf("the activity of branch %q cannot be read", branch)
	case h.Created:
		return ""
	case h.Rewritten:
		return fmt.Sprintf("the activity of branch %q does not go back to its creation, and has force-pushes", branch)
	case len(h.Heads) > 0 && head != "":
		mb, err := r.git(ctx, "merge-base", head, layer)
		if err != nil {
			break
		}
		// The layer has the oldest head recorded, and the branch only moved forward since.
		if ok, err := r.gitTest(ctx, "merge-base", "--is-ancestor", h.Heads[len(h.Heads)-1], mb); err == nil && ok {
			return ""
		}
	}
	return fmt.Sprintf("the activity of branch %q does not go back to the head the layer was built on", branch)
}

// fetchMissing fetches the given commits that the repository does not have, like the heads a branch
// had before it was force-pushed, which GitHub serves by their SHA.
func (r *restacker) fetchMissing(ctx context.Context, commits []string) error {
	if len(commits) == 0 {
		return nil
	}
	out, err := r.gitInput(ctx, strings.NewReader(strings.Join(commits, "\n")+"\n"), "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return err
	}
	var missing []string
	for l := range strings.SplitSeq(string(out), "\n") {
		if c, ok := strings.CutSuffix(l, " missing"); ok && !slices.Contains(missing, c) {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// A commit the remote does not have fails the fetch of all of them, so they are fetched one by one then.
	// The commits come from the API, and must not be read as options.
	args := []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", r.remote, "--"}
	if _, err = r.git(ctx, append(args, missing...)...); err == nil || len(missing) == 1 {
		return err
	}
	var errs []error
	for _, c := range missing {
		if _, err := r.git(ctx, append(args, c)...); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// dropped returns the commits up to the fork point that none of the given heads has, nor a counterpart
// of: a commit with the same patch, like after a rebase, or with the same subject and files, like after
// an amend, or the same contents of the files it changes, like after a squash. The commits of the action
// and the changes of atlas.sum are left out, as each restack rewrites them.
func (r *restacker) dropped(ctx context.Context, fork string, heads []string, trunk string) ([]string, error) {
	// The commits the trunk has are not lost, even when the branch below no longer has them.
	out, err := r.git(ctx, append([]string{"log", "--no-show-signature", "--no-merges", "--format=%H %s", fork, "--not", trunk}, heads...)...)
	if err != nil || out == "" {
		return nil, err
	}
	patches := make(map[string]bool) // Commits whose patch a head has.
	for _, h := range heads {
		c, err := r.git(ctx, "cherry", h, fork)
		if err != nil {
			return nil, err
		}
		for l := range strings.SplitSeq(c, "\n") {
			if sha, ok := strings.CutPrefix(l, "- "); ok {
				patches[sha] = true
			}
		}
	}
	s, err := r.git(ctx, append(append([]string{"log", "--no-show-signature", "--no-merges", "--format=%H %s"}, heads...), "--not", fork)...)
	if err != nil {
		return nil, err
	}
	subjects := make(map[string][]string) // Commits the heads have since the fork point, by their subjects.
	for l := range strings.SplitSeq(s, "\n") {
		if c, subject, ok := strings.Cut(l, " "); ok {
			subjects[subject] = append(subjects[subject], c)
		}
	}
	var lost []string
	for l := range strings.SplitSeq(out, "\n") {
		c, subject, _ := strings.Cut(l, " ")
		if patches[c] || subject == r.message {
			continue
		}
		files, err := r.changedFiles(ctx, c)
		if err != nil {
			return nil, err
		}
		// An amend keeps the subject and the files, but may move the migration files to other versions.
		amended := false
		for _, h := range subjects[subject] {
			hfiles, err := r.changedFiles(ctx, h)
			if err != nil {
				return nil, err
			}
			if amended = slices.Equal(r.unversioned(files), r.unversioned(hfiles)); amended {
				break
			}
		}
		if amended {
			continue
		}
		specs := make([]string, len(files))
		for i, f := range files {
			specs[i] = ":(top,literal)" + f
		}
		// Whether a head has the files the commit changes as they are at the fork point.
		kept := len(specs) == 0
		for i := 0; !kept && i < len(heads); i++ {
			if kept, err = r.gitTest(ctx, append([]string{"diff-tree", "-r", "--quiet", fork, heads[i], "--"}, specs...)...); err != nil {
				return nil, err
			}
		}
		if !kept {
			lost = append(lost, fmt.Sprintf("%s (%s)", short(c), subject))
		}
	}
	return lost, nil
}

// changedFiles returns the files the commit changes, other than atlas.sum.
func (r *restacker) changedFiles(ctx context.Context, c string) ([]string, error) {
	b, err := r.gitRaw(ctx, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--no-renames", "--root", c)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(splitNUL(b), func(f string) bool { return f == r.sumPath() }), nil
}

// unversioned returns the sorted files, with the versions of the migration files left out.
func (r *restacker) unversioned(files []string) []string {
	s := make([]string, len(files))
	for i, f := range files {
		if s[i] = f; path.Dir(f) == r.rel && path.Ext(f) == ".sql" {
			_, name, _ := strings.Cut(path.Base(f), "_")
			s[i] = path.Join(r.rel, "_"+name)
		}
	}
	slices.Sort(s)
	return s
}

// splitNUL splits the NUL-terminated output of a git command.
func splitNUL(b []byte) []string {
	return strings.FieldsFunc(string(b), func(r rune) bool { return r == 0 })
}

// firstConflict returns the index of the lowest layer whose migration directory does not
// extend the directory of the layer below it (or of the trunk), or -1 if there is none.
// A layer whose directory only lags behind the layer below it, because the trunk moved,
// conflicts only if a layer at or above it adds migrations of its own. Otherwise, GitHub
// can rebase the stack without conflicts, and the action leaves it to GitHub.
func (r *restacker) firstConflict(ctx context.Context, base, trunk string, layers []*layerState) (int, error) {
	first := -1
	for i, l := range layers {
		parent, parentName := trunk, base
		if i > 0 {
			parent, parentName = layers[i-1].old, fmt.Sprintf("#%d (%s)", layers[i-1].Number, layers[i-1].Branch)
		}
		ok, err := r.upToDate(ctx, parent, l)
		if err != nil {
			return 0, err
		}
		if !ok && first < 0 {
			r.Infof("#%d (%s): migrations conflict with %s", l.Number, l.Branch, parentName)
			first = i
		}
		if first >= 0 {
			// Whether the own commits of the layer change its migrations.
			same, err := r.gitTest(ctx, "diff-tree", "-r", "--quiet", l.fork.commit, l.old, "--", r.sqlSpec(), r.sumSpec())
			if err != nil {
				return 0, err
			}
			if !same {
				return first, nil
			}
		}
	}
	if first >= 0 {
		r.Infof("No pull request at or above #%d adds migrations, GitHub can rebase the stack without conflicts", layers[first].Number)
	} else {
		r.Infof("Migrations of all the pull requests are up to date, nothing to restack")
	}
	return -1, nil
}

// allOnTop reports whether the migrations of each layer are on top of the ones of the layer below
// it, or of the trunk, with the same hashes. Then firstConflict finds no conflict either.
func (r *restacker) allOnTop(ctx context.Context, trunk string, layers []*layerState) (bool, error) {
	parent := trunk
	for _, l := range layers {
		ps, err := r.sumAt(ctx, parent)
		if err != nil {
			return false, err
		}
		cs, err := r.sumAt(ctx, l.old)
		if err != nil {
			return false, err
		}
		if !r.onTop(ps, cs, true) {
			return false, nil
		}
		mb, err := r.git(ctx, "merge-base", parent, l.old)
		if err != nil {
			return false, nil
		}
		if stale, err := r.stale(ctx, mb, parent, ps, cs); err != nil || stale {
			return false, err
		}
		parent = l.old
	}
	return true, nil
}

// upToDate reports whether the migrations of the layer are on top of the ones of the parent commit:
// its atlas.sum starts with all the files of the parent's, and the files it adds have later versions.
// Atlas chains the hashes of the files, so a layer that edits a migration file of the parent has other
// hashes for the files from it on. Such a layer is up to date, as long as the migrations of the parent
// did not change since the layer was built on it.
func (r *restacker) upToDate(ctx context.Context, parent string, l *layerState) (bool, error) {
	ps, err := r.sumAt(ctx, parent)
	if err != nil {
		return false, err
	}
	cs, err := r.sumAt(ctx, l.old)
	if err != nil {
		return false, err
	}
	switch {
	case !r.onTop(ps, cs, false):
		return false, nil
	case r.onTop(ps, cs, true):
		stale, err := r.stale(ctx, l.fork.commit, parent, ps, cs)
		return !stale, err
	default:
		return r.gitTest(ctx, "diff-tree", "-r", "--quiet", l.fork.commit, parent, "--", r.sqlSpec(), r.sumSpec())
	}
}

// onTop reports whether the hash file c starts with all the files of p, with the same hashes
// if requested, and whether none of the files it adds has the version of a file of p. Atlas
// orders the files by name, but rejects a directory with two files of the same version.
func (r *restacker) onTop(p, c migrate.HashFile, hashes bool) bool {
	if len(p) > len(c) {
		return false
	}
	for i := range p {
		if p[i].N != c[i].N || hashes && p[i].H != c[i].H {
			return false
		}
	}
	return !slices.ContainsFunc(c[len(p):], func(f struct{ N, H string }) bool {
		return slices.ContainsFunc(p, func(pf struct{ N, H string }) bool { return fileVersion(pf.N) == fileVersion(f.N) })
	})
}

// stale reports whether the parent changed migrations since the commit the layer was built on, in a way its sums do
// not show: the layer has files on top of the ones of the parent that the commit it was built on already had, like when
// the parent removed its last migrations since, or, in other formats, the parent changed its files without a version.
func (r *restacker) stale(ctx context.Context, base, parent string, ps, cs migrate.HashFile) (bool, error) {
	bs, err := r.sumAt(ctx, base)
	if err != nil {
		return false, err
	}
	switch {
	case slices.ContainsFunc(cs[len(ps):], func(f struct{ N, H string }) bool {
		return slices.ContainsFunc(bs, func(b struct{ N, H string }) bool { return b.N == f.N })
	}):
		return true, nil
	case r.atlas:
		return false, nil
	}
	bb, err := r.blobs(ctx, base)
	if err != nil {
		return false, err
	}
	pb, err := r.blobs(ctx, parent)
	if err != nil {
		return false, err
	}
	versioned := func(name, _ string) bool { return !unversioned(name) }
	maps.DeleteFunc(bb, versioned)
	maps.DeleteFunc(pb, versioned)
	return !maps.Equal(bb, pb), nil
}

// versioned leaves out the files of other formats that have no version, like the repeatable migrations of Flyway
// (R__name.sql), which are applied by name and listed after the versioned files, so their hashes chain after them.
func (r *restacker) versioned(hf migrate.HashFile) migrate.HashFile {
	if r.atlas {
		return hf
	}
	return slices.DeleteFunc(hf, func(f struct{ N, H string }) bool { return unversioned(f.N) })
}

// unversioned reports whether the name is of a migration file without a version, like R__name.sql in Flyway.
func unversioned(name string) bool {
	return path.Ext(name) == ".sql" && !strings.ContainsAny(fileVersion(name), "0123456789")
}

// fileVersion returns the version of a migration file in the Atlas format.
func fileVersion(name string) string {
	return migrate.NewLocalFile(name, nil).Version()
}

// sumAt returns the atlas.sum of the migration directory at the given commit, or nil if it has none.
func (r *restacker) sumAt(ctx context.Context, c string) (migrate.HashFile, error) {
	name := r.sumPath()
	ok, err := r.gitTest(ctx, "rev-parse", "--quiet", "--verify", c+":"+name)
	if err != nil || !ok {
		return nil, err
	}
	hf, err := r.hashFileFrom(ctx, c, name)
	return r.versioned(hf), err
}

// restackLayer replays the own commits of the layer onto the parent commit, and moves its
// own migration files on top of the parent's ones. It returns the new head of the layer.
func (r *restacker) restackLayer(ctx context.Context, l *layerState, parent, parentName string) (string, error) {
	// The rebase skips the commits its upstream has, but not the ones of the commit it replays onto.
	// An upstream that merges both also skips the commits the parent has, like the ones of the trunk
	// that the layer merged, which the trunk may have reverted since.
	args := []string{"commit-tree", "-p", l.fork.upstream}
	for _, h := range l.fork.others {
		args = append(args, "-p", h)
	}
	upstream, err := r.git(ctx, append(args, "-p", parent, "-m", "upstream", parent+"^{tree}")...)
	if err != nil {
		return "", err
	}
	if err := r.checkMerges(ctx, upstream, l); err != nil {
		return "", err
	}
	commits, err := r.git(ctx, "rev-list", "--count", "--no-merges", upstream+".."+l.old)
	if err != nil {
		return "", err
	}
	r.Infof("#%d (%s): replaying %s commit(s) onto %s (%s)", l.Number, l.Branch, commits, parentName, short(parent))
	// The configuration of the machine must not keep the merge commits, or move the local branches.
	if _, err := r.git(ctx, "-c", "merge."+sumMergeDriver+".name=atlas-action", "-c", "merge."+sumMergeDriver+".driver=true",
		"-c", "rebase.rebaseMerges=false", "-c", "rebase.updateRefs=false",
		"rebase", "--quiet", "--onto", parent, upstream, l.old); err != nil {
		out, _ := r.gitRaw(ctx, "diff-files", "--name-only", "-z", "--diff-filter=U")
		if _, aerr := r.git(ctx, "rebase", "--abort"); aerr != nil {
			err = errors.Join(err, aerr)
		}
		if files := splitNUL(out); len(files) > 0 {
			msg := fmt.Sprintf("conflicts with %s in %s", parentName, strings.Join(files, ", "))
			if slices.ContainsFunc(files, func(f string) bool { return path.Base(f) == migrate.HashFileName && f != r.sumPath() }) {
				msg += fmt.Sprintf(". Restacks resolve only %s, and not the %s files of other migration directories", r.sumPath(), migrate.HashFileName)
			}
			return "", errors.New(msg)
		}
		return "", fmt.Errorf("failed to replay its commits onto %s: %w", parentName, err)
	}
	if err := r.Atlas.MigrateHash(ctx, &atlasexec.MigrateHashParams{DirURL: r.dirURL}); err != nil {
		return "", fmt.Errorf("failed to run `atlas migrate hash`: %w", err)
	}
	ps, err := r.sumAt(ctx, parent)
	if err != nil {
		return "", err
	}
	cs, err := r.sumFile()
	if err != nil {
		return "", err
	}
	if !r.onTop(ps, cs, true) {
		if !r.atlas {
			return "", fmt.Errorf("its migrations must be moved on top of the ones of %s, which `atlas migrate rebase` supports only for the %q format", parentName, formatAtlas)
		}
		own, err := r.ownFiles(ctx, l, parent, parentName, ps)
		if err != nil {
			return "", err
		}
		if err := r.waitClock(ctx, ps, parentName); err != nil {
			return "", err
		}
		files, err := r.renameDuplicates(ctx, own, cs)
		if err != nil {
			return "", err
		}
		if err := r.Atlas.MigrateRebase(ctx, &atlasexec.MigrateRebaseParams{DirURL: r.dirURL, Files: files}); err != nil {
			return "", fmt.Errorf("failed to rebase migrations: %w", err)
		}
		if cs, err = r.sumFile(); err != nil {
			return "", err
		}
		if !r.onTop(ps, cs, true) {
			return "", fmt.Errorf("its migrations are not on top of the ones of %s after `atlas migrate rebase`", parentName)
		}
		moved := slices.Clone(own)
		// The files keep their order, after the ones of the parent.
		if added := cs[len(ps):]; len(added) == len(own) {
			for i := range moved {
				moved[i] = fmt.Sprintf("%s to %s", own[i], added[i].N)
			}
		}
		r.Infof("#%d (%s): renamed %s, to place them on top of %s", l.Number, l.Branch, strings.Join(moved, ", "), parentName)
	}
	if _, err := r.git(ctx, "add", "--all", "--", r.sqlSpec(), r.sumSpec()); err != nil {
		return "", fmt.Errorf("failed to stage changes: %w", err)
	}
	switch same, err := r.gitTest(ctx, "diff-index", "--cached", "--quiet", "HEAD", "--"); {
	case err != nil:
		return "", err
	case same:
	default:
		head, err := r.git(ctx, "rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		args := []string{"commit", "--quiet", "--message", r.message}
		// Amend the commit of an earlier run, so each layer keeps a single commit of the action.
		if subject, err := r.git(ctx, "log", "--no-show-signature", "--max-count=1", "--format=%s", "HEAD"); err == nil && subject == r.message && head != parent {
			args = []string{"commit", "--quiet", "--amend", "--no-edit"}
		}
		if _, err := r.git(ctx, args...); err != nil {
			return "", fmt.Errorf("failed to commit changes: %w", err)
		}
	}
	return r.git(ctx, "rev-parse", "HEAD")
}

// renameDuplicates gives the own files that have the version of another file a temporary version,
// as Atlas cannot read a directory with two files of the same version, and returns the new names of
// the own files. `atlas migrate rebase` then moves them on top, like the others.
func (r *restacker) renameDuplicates(ctx context.Context, own []string, cs migrate.HashFile) ([]string, error) {
	files := slices.Clone(own)
	// Files per version, including the ones excluded from atlas.sum.
	count := make(map[string]int, len(cs))
	for _, f := range cs {
		count[fileVersion(f.N)]++
	}
	for _, name := range own {
		if !slices.ContainsFunc(cs, func(f struct{ N, H string }) bool { return f.N == name }) {
			count[fileVersion(name)]++
		}
	}
	renamed := false
	for i, name := range own {
		v := fileVersion(name)
		if count[v] < 2 {
			continue
		}
		next := v + "0"
		for count[next] > 0 {
			next += "0"
		}
		count[v]--
		count[next]++
		files[i] = next + strings.TrimPrefix(name, v)
		if err := os.Rename(filepath.Join(r.dir, name), filepath.Join(r.dir, files[i])); err != nil {
			return nil, err
		}
		renamed = true
	}
	if renamed {
		if err := r.Atlas.MigrateHash(ctx, &atlasexec.MigrateHashParams{DirURL: r.dirURL}); err != nil {
			return nil, fmt.Errorf("failed to run `atlas migrate hash`: %w", err)
		}
	}
	return files, nil
}

// checkMerges fails if a merge commit that the restack replays has changes of its own, as
// a rebase drops merge commits, and replays only the commits they merged.
func (r *restacker) checkMerges(ctx context.Context, upstream string, l *layerState) error {
	out, err := r.git(ctx, "rev-list", "--merges", "--parents", upstream+".."+l.old)
	if err != nil {
		return err
	}
	for line := range strings.SplitSeq(out, "\n") {
		m, parents, _ := strings.Cut(line, " ")
		if m == "" {
			continue
		}
		// Git cannot merge the parents of an octopus merge again, to find its own changes.
		if n := len(strings.Fields(parents)); n > 2 {
			return fmt.Errorf("merge commit %s merges %d commits, and a restack drops it without checking it for changes of its own", short(m), n)
		}
		// The changes of a merge commit, compared with merging its parents again. Exact renames of migration files, like
		// the ones of the action's rebase of a pull request that is not stacked, are left out: the restack moves them again.
		b, err := r.gitRaw(ctx, "show", "--no-show-signature", "--no-relative", "--remerge-diff", "--format=", "--name-status", "-M100%", "-z", m)
		if err != nil {
			return fmt.Errorf("failed to check merge commit %s, which a restack drops (git 2.36 or later is required): %w", short(m), err)
		}
		var files []string
		for fs := splitNUL(b); len(fs) > 1; fs = fs[2:] {
			status, name := fs[0], fs[1]
			if status[0] == 'R' || status[0] == 'C' {
				if fs = fs[1:]; len(fs) < 2 {
					break
				}
				if status == "R100" && r.migrationFile(name) && r.migrationFile(fs[1]) {
					// A file of the merged branch, like a migration of the trunk, is not the layer's to rename.
					if merged, err := r.gitTest(ctx, "rev-parse", "--verify", "--quiet", m+"^2:"+name); err != nil {
						return err
					} else if !merged {
						continue
					}
				}
				name = fs[1]
			}
			if name != r.sumPath() {
				files = append(files, name)
			}
		}
		if len(files) > 0 {
			return fmt.Errorf("merge commit %s has changes of its own in %s, which a restack drops", short(m), strings.Join(files, ", "))
		}
	}
	return nil
}

// ownFiles returns the migration files the layer adds on top of the parent, and fails if
// the layer edits, removes or duplicates the parent's migration files, as these cannot be
// fixed by moving the layer's files. A file is a duplicate if the restack created it: it
// has the contents of a file of the parent, unlike the files the layer had before.
func (r *restacker) ownFiles(ctx context.Context, l *layerState, parent, parentName string, ps migrate.HashFile) ([]string, error) {
	ptree, err := r.blobs(ctx, parent)
	if err != nil {
		return nil, err
	}
	ctree, err := r.blobs(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	otree, err := r.blobs(ctx, l.old)
	if err != nil {
		return nil, err
	}
	// Blobs of the parent's migration files, which the layer must not copy.
	pblobs := make(map[string]string, len(ps))
	for _, f := range ps {
		pblobs[f.N] = ptree[f.N]
	}
	var own, edited, removed, dups []string
	for _, f := range ps {
		switch b, ok := ctree[f.N]; {
		case !ok:
			removed = append(removed, f.N)
		case b != pblobs[f.N]:
			edited = append(edited, f.N)
		}
	}
	count := func(tree map[string]string, blob string) (n int) {
		for _, b := range tree {
			if b == blob {
				n++
			}
		}
		return n
	}
	add := func(name string) {
		b := ctree[name]
		// A layer may have copies of its own, like a migration it adds again, but the restack must not add any.
		if n := blobName(pblobs, b); n != "" && count(ctree, b) > count(otree, b) {
			dups = append(dups, fmt.Sprintf("%s (same as %s)", name, n))
		} else {
			own = append(own, name)
		}
	}
	// Including the files excluded from atlas.sum, as their versions still sort them.
	for name := range ctree {
		if _, ok := ptree[name]; !ok && path.Ext(name) == ".sql" {
			add(name)
		}
	}
	switch {
	case len(edited) > 0:
		return nil, fmt.Errorf("it edits migration files of %s: %s", parentName, strings.Join(edited, ", "))
	case len(removed) > 0:
		return nil, fmt.Errorf("it removes migration files of %s: %s", parentName, strings.Join(removed, ", "))
	case len(dups) > 0:
		slices.Sort(dups)
		return nil, fmt.Errorf("it has copies of migration files of %s: %s", parentName, strings.Join(dups, ", "))
	case len(own) == 0:
		return nil, fmt.Errorf("its migration directory does not extend the one of %s", parentName)
	}
	slices.Sort(own)
	return own, nil
}

// blobName returns the name of the file with the given blob, the first in sorted order
// if several files have it, or an empty string if none has it.
func blobName(blobs map[string]string, blob string) string {
	var name string
	for n, b := range blobs {
		if b == blob && (name == "" || n < name) {
			name = n
		}
	}
	return name
}

// blobs returns the blob of each file in the migration directory at the given commit.
func (r *restacker) blobs(ctx context.Context, c string) (map[string]string, error) {
	args := []string{"ls-tree", "-z", "--full-tree", c}
	if r.rel != "." {
		args = append(args, "--", r.rel+"/")
	}
	out, err := r.gitRaw(ctx, args...)
	if err != nil {
		return nil, err
	}
	blobs := make(map[string]string)
	for e := range bytes.SplitSeq(out, []byte{0}) {
		// <mode> SP <type> SP <object> TAB <file>
		meta, name, ok := strings.Cut(string(e), "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 && f[1] == "blob" {
			blobs[path.Base(name)] = f[2]
		}
	}
	return blobs, nil
}

// waitClock waits for the clock to pass the latest migration version of the parent, because
// `atlas migrate rebase` versions the files it moves from the current time. The versions it
// writes for many files run ahead of the clock, and may overflow their fields, like
// 20261123222860, but they still sort after the versions of earlier times.
func (r *restacker) waitClock(ctx context.Context, ps migrate.HashFile, parentName string) error {
	if len(ps) == 0 {
		return nil
	}
	latest := ps[len(ps)-1].N
	// Versions that are not timestamps are not waited for. The files may then not end up on top.
	v := fileVersion(latest)
	if len(v) != len(versionLayout) || strings.Trim(v, "0123456789") != "" {
		return nil
	}
	if v >= r.now().UTC().Add(maxClockWait).Format(versionLayout) {
		return fmt.Errorf("the latest migration of %s, %s, is versioned more than %s ahead of the clock", parentName, latest, maxClockWait)
	}
	if v < r.now().UTC().Format(versionLayout) {
		return nil
	}
	r.Infof("Waiting for the clock to pass the latest migration version of %s, %s", parentName, v)
	for v >= r.now().UTC().Format(versionLayout) {
		if err := r.sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	return nil
}

// sumFile reads the atlas.sum of the migration directory in the working tree.
func (r *restacker) sumFile() (migrate.HashFile, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, migrate.HashFileName))
	if err != nil {
		return nil, err
	}
	var hf migrate.HashFile
	if err := hf.UnmarshalText(b); err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", migrate.HashFileName, err)
	}
	return r.versioned(hf), nil
}

// fetch fetches the trunk, the open layers and the head of the top merged layer.
func (r *restacker) fetch(ctx context.Context, base string, merged *StackLayer, layers []*layerState) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", r.remote, fmt.Sprintf("+refs/heads/%s:%s", base, r.remoteRef(base))}
	for _, l := range layers {
		args = append(args, fmt.Sprintf("+refs/heads/%s:%s", l.Branch, r.remoteRef(l.Branch)))
	}
	if _, err := r.git(ctx, args...); err != nil {
		return fmt.Errorf("failed to fetch the branches of the stack: %w", err)
	}
	for _, l := range layers {
		var err error
		if l.old, err = r.git(ctx, "rev-parse", "--verify", "--quiet", r.remoteRef(l.Branch)+"^{commit}"); err != nil {
			return fmt.Errorf("failed to resolve the branch %q: %w", l.Branch, err)
		}
		l.new = l.old
	}
	if merged != nil {
		// GitHub keeps the head of merged pull requests, even after their branch is deleted.
		if _, err := r.git(ctx, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", r.remote,
			fmt.Sprintf("+refs/pull/%d/head:%s", merged.Number, r.mergedRef(merged))); err != nil {
			r.Warningf("failed to fetch the head of the merged pull request #%d: %v", merged.Number, err)
		}
	}
	return nil
}

// push pushes the restacked branches at once, if the remote branches did not move since they were fetched.
func (r *restacker) push(ctx context.Context, s *Stack, layers []*layerState) error {
	var leases, refs, names []string
	for _, l := range layers {
		if l.old == l.new {
			continue
		}
		leases = append(leases, fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", l.Branch, l.old))
		refs = append(refs, fmt.Sprintf("%s:refs/heads/%s", l.new, l.Branch))
		names = append(names, fmt.Sprintf("%s (%s..%s)", l.Branch, short(l.old), short(l.new)))
	}
	if len(refs) == 0 {
		r.Infof("Nothing to push")
		return nil
	}
	args := append(append([]string{"push", "--atomic"}, leases...), r.remote)
	if _, err := r.git(ctx, append(args, refs...)...); err != nil {
		for _, l := range layers {
			l.new = l.old
		}
		// A branch was pushed after it was fetched. The workflow run of that
		// push, or the next one, restacks the stack with the new commits.
		if strings.Contains(err.Error(), "(stale info)") {
			r.Warningf("Stack #%d changed while it was restacked, nothing was pushed: %v", s.Number, err)
			return nil
		}
		return fmt.Errorf("failed to push %s (nothing was pushed): %w", strings.Join(names, ", "), err)
	}
	r.Infof("Pushed %s", strings.Join(names, ", "))
	return nil
}

// installMergeDriver sets the merge driver of the atlas.sum file in the repository
// attributes, which take precedence over the .gitattributes files. It returns a
// function that restores the attributes.
func (r *restacker) installMergeDriver(ctx context.Context) (func() error, error) {
	name, err := r.git(ctx, "rev-parse", "--git-path", "info/attributes")
	if err != nil {
		return nil, err
	}
	orig, err := os.ReadFile(name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	existed := err == nil
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return nil, err
	}
	b := slices.Clone(orig)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = fmt.Appendf(b, "/%s merge=%s\n", path.Join(r.rel, migrate.HashFileName), sumMergeDriver)
	if err := os.WriteFile(name, b, 0644); err != nil {
		return nil, err
	}
	return func() error {
		if !existed {
			return os.Remove(name)
		}
		return os.WriteFile(name, orig, 0644)
	}, nil
}

// discard discards the changes in the working tree, and a merge in progress, which "git reset --hard" also ends.
func (r *restacker) discard(ctx context.Context) error {
	if _, err := r.git(ctx, "reset", "--quiet", "--hard"); err != nil {
		return err
	}
	return r.clean(ctx)
}

// clean removes the migration files that git does not track, which a failed restack may leave.
// The migration directory had none when the run started.
func (r *restacker) clean(ctx context.Context) error {
	_, err := r.git(ctx, "clean", "--quiet", "--force", "--", r.sqlSpec())
	return err
}

// migrationFile reports whether the path, relative to the repository root, is a migration file of the directory.
func (r *restacker) migrationFile(name string) bool {
	return path.Dir(name) == r.rel && path.Ext(name) == ".sql"
}

// sqlSpec returns the pathspec of the migration files.
func (r *restacker) sqlSpec() string {
	return ":(top,glob)" + path.Join(r.rel, "*.sql")
}

// sumPath returns the path of the atlas.sum file, relative to the repository root.
func (r *restacker) sumPath() string {
	return path.Join(r.rel, migrate.HashFileName)
}

// sumSpec returns the pathspec of the atlas.sum file.
func (r *restacker) sumSpec() string {
	return ":(top,literal)" + path.Join(r.rel, migrate.HashFileName)
}

// remoteRef returns the remote-tracking ref of the branch.
func (r *restacker) remoteRef(branch string) string {
	return fmt.Sprintf("refs/remotes/%s/%s", r.remote, branch)
}

// mergedRef returns the local ref of the head of a merged pull request.
func (r *restacker) mergedRef(l *StackLayer) string {
	return fmt.Sprintf("refs/atlas-action/pull/%d/head", l.Number)
}

// git runs a git command and returns its trimmed output.
func (r *restacker) git(ctx context.Context, args ...string) (string, error) {
	out, err := r.gitRaw(ctx, args...)
	return strings.TrimSpace(string(out)), err
}

// gitRaw runs a git command and returns its output.
func (r *restacker) gitRaw(ctx context.Context, args ...string) ([]byte, error) {
	return r.gitInput(ctx, nil, args...)
}

// gitInput runs a git command with the given standard input, and returns its output.
func (r *restacker) gitInput(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	cmd := r.CmdExecutor(ctx, "git", args...)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", gitCommand(args), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", gitCommand(args), err)
	}
	return out, nil
}

// gitTest runs a git command that reports a condition with its exit code: 0 for true, and 1 for false.
func (r *restacker) gitTest(ctx context.Context, args ...string) (bool, error) {
	_, err := r.gitRaw(ctx, args...)
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// gitCommand returns the git command in the arguments, after the global options.
func gitCommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case !strings.HasPrefix(args[i], "-"):
			return args[i]
		}
	}
	return strings.Join(args, " ")
}

// describeStack describes the open layers of a stack, from the trunk up.
func describeStack(base string, layers []*layerState) string {
	var b strings.Builder
	b.WriteString(base)
	for _, l := range layers {
		fmt.Fprintf(&b, " <- #%d (%s)", l.Number, l.Branch)
	}
	return b.String()
}

func stackNumbers(nums []int) string {
	s := make([]string, len(nums))
	for i, n := range nums {
		s[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(s, ", ")
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
