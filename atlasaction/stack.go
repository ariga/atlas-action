// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	// version it did not write itself, before it stamps the migrations above it.
	maxClockWait = 5 * time.Minute
	// stackHint is appended to the errors that leave a stack untouched.
	stackHint = "Nothing was pushed. Restack it locally with `gh stack rebase`, then push it with `gh stack push`"
)

type (
	// restacker restacks stacked pull requests whose migration directories conflict.
	restacker struct {
		*Actions
		tc      *TriggerContext
		sc      StackClient
		branch  string // Branch the action runs on.
		remote  string
		dirURL  string
		dir     string // Migration directory, relative to the working directory.
		rel     string // Migration directory, relative to the repository root.
		atlas   bool   // Whether the directory has the default Atlas format.
		message string // Message of the commits that fix the migration directory.
		now     func() time.Time
		sleep   func(context.Context, time.Duration) error
		// last is the latest migration file this run moved, as the files moved for the
		// next stacks must get later versions, even if they do not share a parent.
		last string
		// moved holds the heads the branches had before this run restacked them,
		// for the stacks that are based on them.
		moved map[string]string
		// basePRs caches the open pull request of each base branch, or 0 if it has none.
		basePRs map[string]int
		// history caches the heads the branch of each pull request had, by its number, for the
		// stacks that share the branch, like the trunk of several stacks. It is nil if unknown.
		history map[int][]string
		// noActivity reports whether the activity of the branches cannot be read in this run.
		noActivity bool
	}
	// layerState is the state of a layer while its stack is restacked.
	layerState struct {
		StackLayer
		old, new string // Head commit before and after the restack.
		fork     *fork  // Where the own commits of the layer start.
		stamped  bool   // Whether its migration files got new versions in this run.
	}
	// fork is the commit a layer was built on: the newest commit of the
	// layer below it, or of the trunk, that the layer has.
	fork struct {
		commit   string // The fork point.
		upstream string // The upstream to replay the commits after the fork point from.
		trusted  bool   // Whether the fork point is known to be exact.
		reason   string // Why the fork point is not trusted, if known.
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
	// Some CI platforms report the branch as a full ref.
	branch := strings.TrimPrefix(tc.Branch, "refs/heads/")
	if branch == "" {
		return errors.New("failed to determine the branch the action runs on")
	}
	tc = func(c TriggerContext) *TriggerContext { c.Branch = branch; return &c }(*tc)
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
	if le := (*listError)(nil); errors.As(err, &le) {
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

// targetStacks returns the open stacks that target the branch and are not seen yet, sorted by their
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
		case s.Open && s.Base == branch:
			stacks = append(stacks, s)
		}
	}
	if len(missing) > 0 && len(missing) == lookups {
		slices.Sort(missing)
		errs = append(errs, fmt.Errorf("none of the stacks of the pull requests that target %q was found (%s)", branch, stackNumbers(missing)))
	}
	slices.SortFunc(stacks, func(a, b *Stack) int { return a.Number - b.Number })
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
		Actions: a,
		tc:      tc,
		sc:      sc,
		branch:  tc.Branch,
		remote:  a.GetInputDefault("remote", "origin"),
		dirURL:  u.String(),
		dir:     filepath.Join(u.Host, u.Path),
		atlas:   u.Query().Get("format") == "" || u.Query().Get("format") == formatAtlas,
		moved:   make(map[string]string),
		basePRs: make(map[string]int),
		history: make(map[int][]string),
		now:     time.Now,
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
	if v, err := r.git(ctx, "--version"); err != nil {
		return nil, err
	} else {
		a.Infof("auto-rebase stacks with %s", v)
	}
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
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != abs {
			return "", fmt.Errorf("restacking does not support a migration directory that is a symbolic link, or under one: %q", dir)
		}
	} else if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if t, err := filepath.EvalSymlinks(top); err == nil {
		top = t
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
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
	done := false
	finish := func() error {
		if done {
			return nil
		}
		done = true
		err := restore()
		args := start
		if current != "" {
			args = []string{"switch", "--quiet", "--discard-changes", "--force-create", r.branch, current}
		}
		if _, cerr := r.git(ctx, args...); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return errors.Join(err, r.clean(ctx))
	}
	defer func() {
		if ferr := finish(); ferr != nil {
			err = errors.Join(err, ferr)
		}
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
			if l.Branch == r.branch {
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
	return pushed, current, errors.Join(append(errs, finish())...)
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
			if len(layers) > 0 {
				return nil, fmt.Errorf("pull request #%d is merged, but #%d below it is open", l.Number, layers[len(layers)-1].Number)
			}
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
	// The branches below the layers, by the numbers of their pull requests.
	branches := make(map[int]string)
	if merged != nil {
		branches[merged.Number] = merged.Branch
	}
	for _, l := range layers[:len(layers)-1] {
		branches[l.Number] = l.Branch
	}
	switch n, err := r.basePR(ctx, s.Base); {
	case err != nil:
		return nil, err
	case n != 0:
		branches[n] = s.Base
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
		if !l.fork.trusted {
			reason := l.fork.reason
			if reason == "" {
				reason = fmt.Sprintf("cannot find the commit of %s it was built on", belowName(s.Base, below, merged))
			}
			return nil, fmt.Errorf("#%d (%s): %s. %s", l.Number, l.Branch, reason, stackHint)
		}
		if l.new, err = r.restackLayer(ctx, l, below, parent, parentName); err != nil {
			// Discard the changes of the failed layer, before the next stack is restacked.
			if _, rerr := r.git(ctx, "reset", "--quiet", "--hard"); rerr != nil {
				err = errors.Join(err, rerr)
			}
			if cerr := r.clean(ctx); cerr != nil {
				err = errors.Join(err, cerr)
			}
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

// basePR returns the number of an open pull request of the base branch, or 0 if it has none.
// Such a base branch may be rewritten, like the branches of a stack.
func (r *restacker) basePR(ctx context.Context, base string) (int, error) {
	if n, ok := r.basePRs[base]; ok {
		return n, nil
	}
	prs, err := r.sc.OpenPullRequests(ctx, base, "")
	if err != nil {
		return 0, fmt.Errorf("failed to list the open pull requests of the base branch %q: %w", base, err)
	}
	n := 0
	// The oldest pull request has the longest history.
	for _, pr := range prs {
		if n == 0 || pr.Number < n {
			n = pr.Number
		}
	}
	r.basePRs[base] = n
	return n, nil
}

// histories returns the heads the branches of the given pull requests had, by the numbers of the pull
// requests. It reads them from the activity of the repository, which GitHub records since the creation
// of the branch, or else from the force-pushes of the pull request, which GitHub records only since it
// was opened. The pull requests whose history cannot be read are left out.
func (r *restacker) histories(ctx context.Context, branches map[int]string) map[int][]string {
	var (
		history = make(map[int][]string, len(branches))
		read    []int // Pull requests whose history is not cached.
		rest    []int // Pull requests whose branch activity cannot be read in full.
	)
	for _, n := range slices.Sorted(maps.Keys(branches)) {
		if h, ok := r.history[n]; ok {
			if h != nil {
				history[n] = h
			}
			continue
		}
		read = append(read, n)
		if r.noActivity {
			rest = append(rest, n)
			continue
		}
		heads, complete, err := r.sc.BranchHeads(ctx, branches[n])
		switch {
		case errors.Is(err, github.ErrNotFound):
			// The activity is not available, like on older versions of GitHub Enterprise Server.
			r.Warningf("failed to read the activity of branch %q, using the force-pushes of the pull requests: %v", branches[n], err)
			r.noActivity = true
			rest = append(rest, n)
		case err != nil:
			// Like a rate limit. The force-pushes may lack heads that the activity has, and lead to
			// another restack, so the history is left unknown, and a later run reads it again.
			r.Warningf("failed to read the activity of branch %q: %v", branches[n], err)
		case !complete:
			r.Infof("The activity of branch %q does not go back to its creation, using the force-pushes of #%d", branches[n], n)
			rest = append(rest, n)
		default:
			history[n] = heads
		}
	}
	for n, h := range r.forcePushes(ctx, rest) {
		history[n] = h
	}
	for _, n := range read {
		h, ok := history[n]
		if ok && h == nil {
			h = []string{} // Known, without heads.
		}
		r.history[n] = h
	}
	return history
}

// forcePushes returns the heads the given pull requests had before and after each
// force-push of their branches, since they were opened. The pull requests whose
// history GitHub cannot tell are left out.
func (r *restacker) forcePushes(ctx context.Context, nums []int) map[int][]string {
	if len(nums) == 0 {
		return map[int][]string{}
	}
	pushes, err := r.sc.ForcePushes(ctx, nums)
	if err != nil {
		r.Warningf("failed to get the force-pushes of the pull requests: %v", err)
		return nil
	}
	for _, n := range nums {
		if _, ok := pushes[n]; !ok {
			r.Warningf("failed to get the force-pushes of pull request #%d", n)
		}
	}
	return pushes
}

// forkPoint returns where the own commits of the layer start: the newest merge-base of the layer
// with the heads the branch below it had. For the bottom layer, these are the trunk, the heads of
// the merged pull request below it, if any, the head the trunk had before the push that triggered
// the run, and the heads of the trunk, if it has a pull request of its own. For the others, the
// head the layer below has, the heads its branch had, the head it had before the push that
// triggered the run, and the head GitHub last compared the pull request with.
//
// The activity of a branch records every head it had since it was created, so with it the fork
// point is exact. The force-pushes of a pull request record the heads its branch had only since the
// pull request was opened, and every other head is an ancestor of a later one, so with them the fork
// point is exact unless the branch was rewritten before its pull request was opened. Without either,
// the fork point is not trusted, as it cannot tell commits the branch below rewrote or dropped from
// the own commits of the layer. Like `gh stack rebase`, the restack does not replay the commits of
// the branch below into the layer, but it refuses when the branch below no longer has some of them
// in any form, as they may have been moved to the layer, like when a branch is split into a stack.
// Commits the branch below had only locally are the layer's own.
func (r *restacker) forkPoint(ctx context.Context, s *Stack, l, below *layerState, merged *StackLayer, trunk string, history map[int][]string) (*fork, error) {
	var (
		f     = &fork{}
		tips  []string
		now   []string // Heads the branches below have now.
		lower []string // Heads of the branches below, before this run restacked them.
		skip  string   // Head of the branch below, whose patches the replay skips.
		known = true   // Whether the force-push history of the branches below is known.
		hist  = func(n int) {
			h, ok := history[n]
			tips, known = append(tips, h...), known && ok
		}
	)
	switch {
	case below != nil:
		tips, lower, skip = append(tips, below.old), append(lower, below.old), below.old
		now = append(now, below.old)
		if r.tc.Before != "" && r.branch == below.Branch && r.tc.Commit == below.old {
			tips = append(tips, r.tc.Before)
		}
		if l.BaseCommit != "" && l.Base == below.Branch {
			tips = append(tips, l.BaseCommit)
		}
		hist(below.Number)
	default:
		now = append(now, trunk)
		if merged != nil {
			if head, err := r.git(ctx, "rev-parse", "--verify", "--quiet", r.mergedRef(merged)+"^{commit}"); err == nil {
				tips, lower, now = append(tips, head), append(lower, head), append(now, head)
			}
			hist(merged.Number)
		}
		// A trunk without a pull request is expected to only move forward, but the layer may
		// hold commits of the merged pull request below it, that are not in the trunk.
		mb, err := r.git(ctx, "merge-base", trunk, l.old)
		if err != nil {
			return nil, err
		}
		f.commit, f.upstream = mb, trunk
		// The head the trunk had before this run restacked it, as a branch of another stack.
		start := trunk
		if old, ok := r.moved[s.Base]; ok {
			start = old
			tips, lower, skip = append(tips, old), append(lower, old), old
		}
		// The push that triggered the run may have rewritten the trunk, even one without a pull request.
		if r.tc.Before != "" && r.branch == s.Base && r.tc.Commit == start {
			tips = append(tips, r.tc.Before)
		}
		n, err := r.basePR(ctx, s.Base)
		if err != nil {
			return nil, err
		}
		if n != 0 {
			// The trunk is the branch of a pull request, that may be rewritten.
			if l.BaseCommit != "" && l.Base == s.Base {
				tips = append(tips, l.BaseCommit)
			}
			hist(n)
		}
	}
	for _, t := range tips {
		// The head may not exist locally, if the layer does not have it.
		mb, err := r.git(ctx, "merge-base", t, l.old)
		if err != nil || mb == f.commit {
			continue
		}
		if f.commit != "" {
			if newer, err := r.gitTest(ctx, "merge-base", "--is-ancestor", f.commit, mb); err != nil || !newer {
				continue
			}
		}
		// Replaying from a head of the branch below also skips the commits it has the patches of.
		f.commit, f.upstream = mb, mb
		if t == skip {
			f.upstream = skip
		}
	}
	if f.commit == "" {
		return nil, fmt.Errorf("#%d (%s): no commit in common with %s", l.Number, l.Branch, belowName("", below, merged))
	}
	// The branch below dropped these commits, or they were moved to the layer. Git cannot tell which.
	dropped, err := r.dropped(ctx, f.commit, now)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 {
		f.reason = fmt.Sprintf("it has commits that %s no longer has, which a restack would drop: %s",
			belowName(s.Base, below, merged), strings.Join(dropped, ", "))
		return f, nil
	}
	if f.trusted = known; !known || len(lower) == 0 {
		return f, nil
	}
	// A migration file that the layer and the branch below both added since the fork point means
	// that the layer has commits the branch below had before it was rewritten, which GitHub did
	// not record, like before its pull request was opened when its activity cannot be read.
	files, err := r.addedByBoth(ctx, f.commit, l.old, lower)
	if err != nil {
		return nil, err
	}
	if len(files) > 0 {
		f.trusted = false
		f.reason = fmt.Sprintf("cannot tell its commits from the ones of %s, as both add %s with different contents",
			belowName(s.Base, below, merged), strings.Join(files, ", "))
	}
	return f, nil
}

// dropped returns the commits up to the fork point that none of the given heads has, nor a counterpart
// of: a commit with the same patch, like after a rebase, or with the same subject, like after an amend,
// or the same contents of the files it changes, like after a squash. The commits of the action and the
// changes of atlas.sum are left out, as each restack rewrites them.
func (r *restacker) dropped(ctx context.Context, fork string, heads []string) ([]string, error) {
	out, err := r.git(ctx, append([]string{"log", "--no-merges", "--format=%H %s", fork, "--not"}, heads...)...)
	if err != nil || out == "" {
		return nil, err
	}
	patches := make(map[string]bool) // Commits whose patch a head has.
	for _, h := range heads {
		c, err := r.git(ctx, "cherry", h, fork)
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(c, "\n") {
			if sha, ok := strings.CutPrefix(l, "- "); ok {
				patches[sha] = true
			}
		}
	}
	s, err := r.git(ctx, append(append([]string{"log", "--no-merges", "--format=%s"}, heads...), "--not", fork)...)
	if err != nil {
		return nil, err
	}
	subjects := make(map[string]bool) // Subjects of the commits the heads have since the fork point.
	for _, l := range strings.Split(s, "\n") {
		subjects[l] = l != ""
	}
	var lost []string
	for _, l := range strings.Split(out, "\n") {
		c, subject, _ := strings.Cut(l, " ")
		if patches[c] || subjects[subject] || subject == r.message {
			continue
		}
		b, err := r.gitRaw(ctx, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--no-renames", "--root", c)
		if err != nil {
			return nil, err
		}
		var specs []string
		for _, f := range splitNUL(b) {
			if f != r.sumPath() {
				specs = append(specs, ":(top,literal)"+f)
			}
		}
		// Whether a head has the files the commit changes as they are at the fork point.
		kept := len(specs) == 0
		for i := 0; !kept && i < len(heads); i++ {
			if kept, err = r.gitTest(ctx, append([]string{"diff", "--quiet", fork, heads[i], "--"}, specs...)...); err != nil {
				return nil, err
			}
		}
		if !kept {
			lost = append(lost, fmt.Sprintf("%s (%s)", short(c), subject))
		}
	}
	return lost, nil
}

// addedByBoth returns the migration files that the commit c added since the fork point,
// and that one of the given heads has with other contents.
func (r *restacker) addedByBoth(ctx context.Context, fork, c string, heads []string) ([]string, error) {
	out, err := r.gitRaw(ctx, "diff", "--name-only", "-z", "--no-renames", "--diff-filter=A", fork, c, "--", r.sqlSpec())
	if err != nil {
		return nil, err
	}
	added := splitNUL(out)
	if len(added) == 0 {
		return nil, nil
	}
	blobs, err := r.blobs(ctx, c)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, h := range heads {
		hblobs, err := r.blobs(ctx, h)
		if err != nil {
			return nil, err
		}
		for _, name := range added {
			name = path.Base(name)
			if b, ok := hblobs[name]; ok && b != blobs[name] && !slices.Contains(files, name) {
				files = append(files, name)
			}
		}
	}
	slices.Sort(files)
	return files, nil
}

// splitNUL splits the NUL-terminated output of a git command.
func splitNUL(b []byte) []string {
	var s []string
	for _, e := range bytes.Split(b, []byte{0}) {
		if len(e) > 0 {
			s = append(s, string(e))
		}
	}
	return s
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
			same, err := r.gitTest(ctx, "diff", "--quiet", l.fork.commit, l.old, "--", r.sqlSpec(), r.sumSpec())
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
		return true, nil
	default:
		return r.gitTest(ctx, "diff", "--quiet", l.fork.commit, parent, "--", r.sqlSpec(), r.sumSpec())
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
	if !r.atlas {
		return true
	}
	return !slices.ContainsFunc(c[len(p):], func(f struct{ N, H string }) bool {
		return slices.ContainsFunc(p, func(pf struct{ N, H string }) bool { return fileVersion(pf.N) == fileVersion(f.N) })
	})
}

// fileVersion returns the version of a migration file in the Atlas format.
func fileVersion(name string) string {
	v, _, _ := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
	return v
}

// compareVersions compares two versions that are numbers, and reports whether they are.
func compareVersions(a, b string) (int, bool) {
	digits := func(s string) bool {
		return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
	}
	if !digits(a) || !digits(b) {
		return 0, false
	}
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if c := len(a) - len(b); c != 0 {
		return c, true
	}
	return strings.Compare(a, b), true
}

// sumAt returns the atlas.sum of the migration directory at the given commit, or nil if it has none.
func (r *restacker) sumAt(ctx context.Context, c string) (migrate.HashFile, error) {
	name := r.sumPath()
	ok, err := r.gitTest(ctx, "rev-parse", "--quiet", "--verify", c+":"+name)
	if err != nil || !ok {
		return nil, err
	}
	b, err := r.gitRaw(ctx, "show", c+":"+name)
	if err != nil {
		return nil, err
	}
	var hf migrate.HashFile
	if err := hf.UnmarshalText(b); err != nil {
		return nil, fmt.Errorf("failed to read %s at %s: %w", name, short(c), err)
	}
	return hf, nil
}

// restackLayer replays the own commits of the layer onto the parent commit, and moves its
// own migration files on top of the parent's ones. It returns the new head of the layer.
func (r *restacker) restackLayer(ctx context.Context, l, below *layerState, parent, parentName string) (string, error) {
	commits, err := r.git(ctx, "rev-list", "--count", l.fork.commit+".."+l.old)
	if err != nil {
		return "", err
	}
	if err := r.checkMerges(ctx, l); err != nil {
		return "", err
	}
	r.Infof("#%d (%s): replaying %s commit(s) onto %s (%s)", l.Number, l.Branch, commits, parentName, short(parent))
	if _, err := r.git(ctx, "-c", "merge."+sumMergeDriver+".name=atlas-action", "-c", "merge."+sumMergeDriver+".driver=true",
		"rebase", "--quiet", "--onto", parent, l.fork.upstream, l.old); err != nil {
		out, _ := r.gitRaw(ctx, "diff", "--name-only", "-z", "--diff-filter=U")
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
		own, err := r.ownFiles(ctx, l, parent, parentName, ps, cs)
		if err != nil {
			return "", err
		}
		if err := r.waitClock(ctx, ps, parentName, below != nil && below.stamped); err != nil {
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
		l.stamped = true
		if latest := cs[len(cs)-1].N; r.last == "" || versionAfter(latest, r.last) {
			r.last = latest
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
	switch same, err := r.gitTest(ctx, "diff", "--cached", "--quiet"); {
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
		if subject, err := r.git(ctx, "log", "-1", "--format=%s", "HEAD"); err == nil && subject == r.message && head != parent {
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
	if !r.atlas {
		return files, nil
	}
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
func (r *restacker) checkMerges(ctx context.Context, l *layerState) error {
	out, err := r.git(ctx, "rev-list", "--merges", l.fork.upstream+".."+l.old)
	if err != nil {
		return err
	}
	for _, m := range strings.Fields(out) {
		// The changes of a merge commit, compared with merging its parents again.
		b, err := r.gitRaw(ctx, "show", "--remerge-diff", "--format=", "--name-only", "-z", m)
		if err != nil {
			return fmt.Errorf("failed to check merge commit %s, which a restack drops (git 2.36 or later is required): %w", short(m), err)
		}
		files := slices.DeleteFunc(splitNUL(b), func(f string) bool { return f == r.sumPath() })
		if len(files) > 0 {
			return fmt.Errorf("merge commit %s has changes of its own in %s, which a restack drops", short(m), strings.Join(files, ", "))
		}
	}
	return nil
}

// reSumIgnore matches the directive that excludes a migration file from atlas.sum.
var reSumIgnore = regexp.MustCompile(`(?m)^--\s*atlas:sum\s+ignore\s*$`)

// ownFiles returns the migration files the layer adds on top of the parent, and fails if
// the layer edits, removes or duplicates the parent's migration files, as these cannot be
// fixed by moving the layer's files. A file is a duplicate if the restack created it: it
// has the contents of a file of the parent, unlike the files the layer had before.
func (r *restacker) ownFiles(ctx context.Context, l *layerState, parent, parentName string, ps, cs migrate.HashFile) ([]string, error) {
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
	for _, f := range cs {
		if _, ok := pblobs[f.N]; !ok {
			add(f.N)
		}
	}
	// Migration files that are excluded from atlas.sum still have a version that sorts them.
	if r.atlas {
		for name := range ctree {
			if _, ok := ptree[name]; ok || path.Ext(name) != ".sql" || slices.ContainsFunc(cs, func(f struct{ N, H string }) bool { return f.N == name }) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(r.dir, name))
			if err != nil {
				return nil, err
			}
			if reSumIgnore.Match(b) {
				add(name)
			}
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

// blobName returns the name of the file with the given blob, if any.
func blobName(blobs map[string]string, blob string) string {
	var names []string
	for n, b := range blobs {
		if b == blob {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return ""
	}
	return names[0]
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
	for _, e := range bytes.Split(out, []byte{0}) {
		// <mode> SP <type> SP <object> TAB <file>
		meta, name, ok := strings.Cut(string(e), "\t")
		if f := strings.Fields(meta); ok && len(f) == 3 && f[1] == "blob" {
			blobs[path.Base(name)] = f[2]
		}
	}
	return blobs, nil
}

// waitClock waits for the clock to pass the latest migration version of the parent, because
// `atlas migrate rebase` versions the files it moves from the current time. The check that the
// files ended up on top of the parent's ones catches any ordering it does not cover. Versions
// that this run wrote are waited for as long as needed, as they run ahead of the clock by one
// second per file. Others are waited for up to maxClockWait.
func (r *restacker) waitClock(ctx context.Context, ps migrate.HashFile, parentName string, stamped bool) error {
	var (
		now  = r.now()
		wait time.Duration
		of   = parentName
	)
	if len(ps) > 0 {
		latest := ps[len(ps)-1].N
		if t, ok := versionTime(latest); ok {
			if wait = t.Sub(now); wait > maxClockWait && !stamped {
				return fmt.Errorf("the latest migration of %s, %s, is versioned %s ahead of the clock", parentName, latest, wait.Round(time.Second))
			}
		}
	}
	// Files moved for other stacks in this run, which may land before this one.
	if t, ok := versionTime(r.last); ok && t.Sub(now) > wait {
		wait, of = t.Sub(now), "the stacks restacked in this run"
	}
	if wait <= 0 {
		return nil
	}
	r.Infof("Waiting %s for the clock to pass the latest migration version of %s", wait.Round(time.Millisecond), of)
	return r.sleep(ctx, wait)
}

// versionAfter reports whether the migration file a has a later version than b.
func versionAfter(a, b string) bool {
	if c, ok := compareVersions(fileVersion(a), fileVersion(b)); ok {
		return c > 0
	}
	return a > b
}

// versionTime returns the earliest time whose version, in the 20060102150405 format, sorts
// after the version of the migration file, if the version has this format. The versions that
// `atlas migrate rebase` writes may overflow their fields, like 20261123222860 for the 10th file
// moved at 22:28:51, as they are incremented as numbers.
func versionTime(name string) (time.Time, bool) {
	v := strings.TrimLeft(name, "Vv")
	if i := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		v = v[:i]
	}
	if len(v) != len("20060102150405") {
		return time.Time{}, false
	}
	n := func(i, j int) int {
		x, _ := strconv.Atoi(v[i:j])
		return x
	}
	y, mo, d, h, mi, s := n(0, 4), n(4, 6), n(6, 8), n(8, 10), n(10, 12), n(12, 14)
	if mo < 1 || mo > 12 || d < 1 {
		return time.Time{}, false
	}
	// The most significant field that overflows sets the time to the start of the next value of the field above it.
	switch days := time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day(); {
	case d > days:
		return time.Date(y, time.Month(mo)+1, 1, 0, 0, 0, 0, time.UTC), true
	case h > 23:
		return time.Date(y, time.Month(mo), d+1, 0, 0, 0, 0, time.UTC), true
	case mi > 59:
		return time.Date(y, time.Month(mo), d, h+1, 0, 0, 0, time.UTC), true
	case s > 59:
		return time.Date(y, time.Month(mo), d, h, mi+1, 0, 0, time.UTC), true
	default:
		return time.Date(y, time.Month(mo), d, h, mi, s+1, 0, time.UTC), true
	}
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
	return hf, nil
}

// fetch fetches the trunk, the open layers and the head of the top merged layer.
func (r *restacker) fetch(ctx context.Context, base string, merged *StackLayer, layers []*layerState) error {
	args := []string{"fetch", "--quiet", "--no-tags", r.remote, fmt.Sprintf("+refs/heads/%s:%s", base, r.remoteRef(base))}
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
		if _, err := r.git(ctx, "fetch", "--quiet", "--no-tags", r.remote,
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
	cmd := r.CmdExecutor(ctx, "git", append(args, refs...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		for _, l := range layers {
			l.new = l.old
		}
		// A branch was pushed after it was fetched. The workflow run of that
		// push, or the next one, restacks the stack with the new commits.
		if strings.Contains(stderr.String(), "(stale info)") {
			r.Warningf("Stack #%d changed while it was restacked, nothing was pushed:\n%s", s.Number, strings.TrimSpace(stderr.String()))
			return nil
		}
		return fmt.Errorf("failed to push %s (nothing was pushed): %w: %s", strings.Join(names, ", "), err, strings.TrimSpace(stderr.String()))
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
	if err != nil && !os.IsNotExist(err) {
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
		// Abort a rebase that was left in progress, if any.
		if dir, err := r.git(ctx, "rev-parse", "--git-path", "rebase-merge"); err == nil {
			if _, err := os.Stat(dir); err == nil {
				_, _ = r.git(ctx, "rebase", "--abort")
			}
		}
		if !existed {
			return os.Remove(name)
		}
		return os.WriteFile(name, orig, 0644)
	}, nil
}

// discard aborts a merge in progress, and discards the changes in the working tree.
func (r *restacker) discard(ctx context.Context) error {
	if _, err := r.git(ctx, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		if _, err := r.git(ctx, "merge", "--abort"); err != nil {
			return err
		}
	}
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
	cmd := r.CmdExecutor(ctx, "git", args...)
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
	cmd := r.CmdExecutor(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exit := (*exec.ExitError)(nil); errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("git %s: %w: %s", gitCommand(args), err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
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
