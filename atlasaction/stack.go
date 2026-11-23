// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ariga.io/atlas/atlasexec"
	"ariga.io/atlas/sql/migrate"
)

const (
	// sumMergeDriver is the name of the merge driver that resolves the conflicts in
	// atlas.sum while the commits of a layer are replayed. The file is regenerated
	// after the replay, so the driver keeps whichever version is checked out.
	sumMergeDriver = "atlas-sum"
	// maxClockWait is the longest the action waits for the clock to pass the
	// latest migration version of a layer, before it stamps the layer above it.
	maxClockWait = time.Minute
)

// stackHint is appended to the errors that leave a stack untouched.
const stackHint = "Nothing was pushed. Restack it locally with `gh stack rebase`, then push it with `gh stack push`"

type (
	// restacker restacks stacked pull requests whose migration directories conflict.
	restacker struct {
		*Actions
		tc      *TriggerContext
		remote  string
		dirURL  string
		dir     string // Migration directory, relative to the working directory.
		sumPath string // atlas.sum path, relative to the repository root.
		message string // Message of the commits that fix the migration directory.
		now     func() time.Time
		sleep   func(context.Context, time.Duration) error
	}
	// layerState is the state of a layer while its stack is restacked.
	layerState struct {
		StackLayer
		old, new string // Head commit before and after the restack.
	}
)

// migrateAutoRebaseStacks restacks the stacked pull requests of the branch the action runs on,
// or the stacks that target the branch. It reports false if there is nothing to restack and the
// branch should be handled as a single pull request.
func (a *Actions) migrateAutoRebaseStacks(ctx context.Context, tc *TriggerContext) (bool, error) {
	if a.GetInput("base-sha") != "" || a.GetBoolInput("force-rebase") {
		return false, errors.New(`the "stack" input cannot be used with "base-sha" or "force-rebase"`)
	}
	c, err := tc.SCMClient()
	if err != nil {
		return false, fmt.Errorf("failed to create the SCM client: %w", err)
	}
	sc, ok := c.(StackClient)
	if !ok {
		return false, errors.New(`the "stack" input is supported only for repositories hosted on GitHub`)
	}
	stacks, single, err := a.branchStacks(ctx, sc, tc)
	switch {
	case err != nil:
		return false, err
	case single:
		return false, nil
	case len(stacks) == 0:
		a.SetOutput("rebased", "false")
		return true, nil
	}
	r, err := a.newRestacker(ctx, tc)
	if err != nil {
		return false, err
	}
	return true, r.run(ctx, stacks)
}

// branchStacks returns the stacks to restack for the branch. A branch that has
// open pull requests is restacked with its stack, and a branch that has none
// is restacked as the trunk of the stacks that target it. single is true when
// the branch has an open pull request that is not stacked and targets the base
// branch, the case migrate/autorebase handles without the "stack" input.
func (a *Actions) branchStacks(ctx context.Context, sc StackClient, tc *TriggerContext) (_ []*Stack, single bool, _ error) {
	prs, err := sc.OpenPullRequests(ctx, tc.Branch, "")
	if err != nil {
		return nil, false, fmt.Errorf("failed to list the open pull requests of %q: %w", tc.Branch, err)
	}
	if len(prs) > 0 {
		var nums []int
		for _, pr := range prs {
			if pr.Stack != 0 && !slices.Contains(nums, pr.Stack) {
				nums = append(nums, pr.Stack)
			}
		}
		switch len(nums) {
		case 0:
			base := a.GetInputDefault("base-branch", tc.DefaultBranch)
			if slices.ContainsFunc(prs, func(pr *PullRequest) bool { return pr.Base == base }) {
				return nil, true, nil
			}
			a.Infof("Pull request #%d of %q targets %q and is not stacked, nothing to restack", prs[0].Number, tc.Branch, prs[0].Base)
			return nil, false, nil
		case 1:
			s, err := sc.Stack(ctx, nums[0])
			if err != nil {
				return nil, false, fmt.Errorf("failed to get stack #%d: %w", nums[0], err)
			}
			return []*Stack{s}, false, nil
		default:
			slices.Sort(nums)
			return nil, false, fmt.Errorf("branch %q belongs to more than one stack (%s), restack them manually", tc.Branch, stackNumbers(nums))
		}
	}
	// The branch has no open pull requests. It may be the trunk of stacks.
	prs, err = sc.OpenPullRequests(ctx, "", tc.Branch)
	if err != nil {
		return nil, false, fmt.Errorf("failed to list the open pull requests that target %q: %w", tc.Branch, err)
	}
	var stacks []*Stack
	for _, pr := range prs {
		if pr.Stack == 0 || slices.ContainsFunc(stacks, func(s *Stack) bool { return s.Number == pr.Stack }) {
			continue
		}
		s, err := sc.Stack(ctx, pr.Stack)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get stack #%d: %w", pr.Stack, err)
		}
		if s.Open && s.Base == tc.Branch {
			stacks = append(stacks, s)
		}
	}
	if len(stacks) == 0 {
		a.Infof("Branch %q has no open pull requests, and no stacks target it, nothing to restack", tc.Branch)
	}
	slices.SortFunc(stacks, func(a, b *Stack) int { return a.Number - b.Number })
	return stacks, false, nil
}

func (a *Actions) newRestacker(ctx context.Context, tc *TriggerContext) (*restacker, error) {
	u, err := url.Parse(a.GetInputDefault("dir", "file://migrations"))
	if err != nil {
		return nil, fmt.Errorf("failed to parse dir URL: %w", err)
	}
	r := &restacker{
		Actions: a,
		tc:      tc,
		remote:  a.GetInputDefault("remote", "origin"),
		dirURL:  a.GetInputDefault("dir", "file://migrations"),
		dir:     filepath.Join(u.Host, u.Path),
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
	if u.Scheme != "file" {
		return nil, fmt.Errorf(`the "stack" input requires a local migration directory (file://), got %q`, r.dirURL)
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
	rel, err := relPath(top, r.dir)
	if err != nil {
		return nil, err
	}
	r.sumPath = path.Join(rel, migrate.HashFileName)
	return r, nil
}

// relPath returns the path of dir relative to the repository root.
func relPath(top, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	// Resolve symbolic links on both sides, as git reports the real path of the root.
	if t, err := filepath.EvalSymlinks(top); err == nil {
		top = t
	}
	if d, err := filepath.EvalSymlinks(abs); err == nil {
		abs = d
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("migration directory %q is outside of the git repository", dir)
	}
	if strings.ContainsAny(rel, " \t\r\n\"'\\*?[]!#") {
		return "", fmt.Errorf("restacking does not support migration directories with spaces or special characters in their path: %q", rel)
	}
	return filepath.ToSlash(rel), nil
}

// run restacks the given stacks. Each stack is pushed on its own, all its branches
// at once, so a failure leaves its branches as they were and does not stop the
// other stacks.
func (r *restacker) run(ctx context.Context, stacks []*Stack) (err error) {
	head, err := r.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		if head, err = r.git(ctx, "rev-parse", "HEAD"); err != nil {
			return err
		}
	}
	// The migration directory is hashed from the working tree, and reset on failures.
	if out, err := r.git(ctx, "status", "--porcelain", "--untracked-files=no"); err != nil {
		return err
	} else if out != "" {
		return fmt.Errorf("restacking requires a clean working tree, found changes in:\n%s", out)
	}
	if out, err := r.git(ctx, "status", "--porcelain", "--", r.dir); err != nil {
		return err
	} else if out != "" {
		return fmt.Errorf("restacking requires a clean migration directory, found changes in:\n%s", out)
	}
	restore, err := r.installMergeDriver(ctx)
	if err != nil {
		return err
	}
	var (
		errs    []error
		rebased bool
		current string // New head commit of the branch the action runs on, if it was restacked.
		done    bool
	)
	// finish restores the repository attributes, and leaves the branch the
	// action runs on checked out, at its new head if it was restacked.
	finish := func() error {
		if done {
			return nil
		}
		done = true
		err := restore()
		args := []string{"checkout", "--quiet", "--force", head}
		if current != "" {
			args = []string{"checkout", "--quiet", "--force", "-B", r.tc.Branch, current}
		}
		if _, cerr := r.git(ctx, args...); cerr != nil {
			err = errors.Join(err, cerr)
		}
		if _, cerr := r.git(ctx, "clean", "--quiet", "--force", "-d", "--", r.dir); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return err
	}
	defer func() {
		if ferr := finish(); ferr != nil {
			err = errors.Join(err, ferr)
		}
	}()
	for _, s := range stacks {
		layers, err := r.restack(ctx, s)
		if err != nil {
			errs = append(errs, fmt.Errorf("stack #%d: %w", s.Number, err))
			continue
		}
		for _, l := range layers {
			if l.old != l.new {
				rebased = true
				if l.Branch == r.tc.Branch {
					current = l.new
				}
			}
		}
	}
	r.SetOutput("rebased", fmt.Sprint(rebased))
	if err := errors.Join(append(errs, finish())...); err != nil {
		return err
	}
	if current != "" {
		if latest, err := r.Atlas.MigrateLs(ctx, &atlasexec.MigrateLsParams{
			DirURL: r.dirURL,
			Short:  true,
			Latest: true,
		}); err != nil {
			r.Warningf("failed to get latest migration version: %v", err)
		} else {
			r.SetOutput("latest_version", strings.TrimSpace(latest))
		}
	}
	return nil
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
	r.Infof("Stack #%d: %s", s.Number, describeStack(s.Base, layers))
	if err := r.fetch(ctx, s.Base, merged, layers); err != nil {
		return nil, err
	}
	trunk, err := r.git(ctx, "rev-parse", "--verify", "--quiet", r.remoteRef(s.Base)+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("failed to resolve the base branch %q: %w", s.Base, err)
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
		if below != nil && below.old == below.new {
			// The layer below was not restacked, so this one needs it only if its migrations conflict.
			ok, err := r.extends(ctx, parent, l.old)
			if err != nil {
				return nil, err
			}
			if ok {
				r.Infof("#%d (%s): migrations are up to date with %s", l.Number, l.Branch, parentName)
				continue
			}
		}
		upstream, err := r.upstream(ctx, l, below, merged, trunk)
		if err != nil {
			return nil, fmt.Errorf("#%d (%s): %w. %s", l.Number, l.Branch, err, stackHint)
		}
		if l.new, err = r.restackLayer(ctx, l, parent, parentName, upstream); err != nil {
			// Discard the changes of the failed layer, before the next stack is restacked.
			if _, rerr := r.git(ctx, "reset", "--quiet", "--hard"); rerr != nil {
				err = errors.Join(err, rerr)
			}
			if _, rerr := r.git(ctx, "clean", "--quiet", "--force", "-d", "--", r.dir); rerr != nil {
				err = errors.Join(err, rerr)
			}
			return nil, fmt.Errorf("#%d (%s): %w. %s", l.Number, l.Branch, err, stackHint)
		}
	}
	return layers, r.push(ctx, s, layers)
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
		ok, err := r.extends(ctx, parent, l.old)
		if err != nil {
			return 0, err
		}
		if !ok && first < 0 {
			r.Infof("#%d (%s): migrations conflict with %s", l.Number, l.Branch, parentName)
			first = i
		}
		if first >= 0 {
			own, err := r.changesDir(ctx, parent, l.old)
			if err != nil {
				return 0, err
			}
			if own {
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

// extends reports whether the migration directory of the commit c extends the
// directory of the commit parent, meaning its atlas.sum starts with all the lines
// of the parent's atlas.sum.
func (r *restacker) extends(ctx context.Context, parent, c string) (bool, error) {
	ps, err := r.sumAt(ctx, parent)
	if err != nil {
		return false, err
	}
	cs, err := r.sumAt(ctx, c)
	if err != nil {
		return false, err
	}
	return extendsSum(ps, cs), nil
}

// extendsSum reports whether the hash file c starts with all the files of p.
func extendsSum(p, c migrate.HashFile) bool {
	return len(p) <= len(c) && slices.Equal(p, c[:len(p)])
}

// changesDir reports whether the commits of c that are not in parent change the migration directory.
func (r *restacker) changesDir(ctx context.Context, parent, c string) (bool, error) {
	mb, err := r.git(ctx, "merge-base", parent, c)
	if err != nil {
		return false, err
	}
	same, err := r.gitTest(ctx, "diff", "--quiet", mb, c, "--", r.dir)
	return !same, err
}

// sumAt returns the atlas.sum of the migration directory at the given commit, or nil if it has none.
func (r *restacker) sumAt(ctx context.Context, c string) (migrate.HashFile, error) {
	ok, err := r.gitTest(ctx, "cat-file", "-e", c+":"+r.sumPath)
	if err != nil || !ok {
		return nil, err
	}
	b, err := r.gitRaw(ctx, "show", c+":"+r.sumPath)
	if err != nil {
		return nil, err
	}
	var hf migrate.HashFile
	if err := hf.UnmarshalText(b); err != nil {
		return nil, fmt.Errorf("failed to read %s at %s: %w", r.sumPath, short(c), err)
	}
	return hf, nil
}

// upstream returns the commit after which the layer's own commits start: the head of the layer
// below it that the layer was built on. Like `gh stack rebase`, it never guesses, and fails when
// it cannot find such a commit, because replaying commits of the layer below into the layer would
// duplicate them, or bring back the ones the layer below dropped.
func (r *restacker) upstream(ctx context.Context, l, below *layerState, merged *StackLayer, trunk string) (string, error) {
	if below == nil {
		// The bottom open layer, its own commits start after the trunk commit it forked from, or
		// after the head of the merged pull request below it, if GitHub did not rebase it yet.
		if merged != nil {
			head := r.mergedRef(merged)
			if ok, _ := r.gitTest(ctx, "merge-base", "--is-ancestor", head, l.old); ok {
				return head, nil
			}
			// The merged pull request got new commits after the layer was built on it.
			// Its commits that the layer has are the ones it was built on.
			if mb, err := r.git(ctx, "merge-base", head, l.old); err == nil {
				if inTrunk, err := r.gitTest(ctx, "merge-base", "--is-ancestor", mb, trunk); err == nil && !inTrunk {
					return mb, nil
				}
			}
		}
		return r.git(ctx, "merge-base", trunk, l.old)
	}
	var candidates []string
	if below.old != below.new {
		candidates = append(candidates, below.old) // The head of the layer below, before this run restacked it.
	}
	candidates = append(candidates, below.new)
	if r.tc.Before != "" && r.tc.Branch == below.Branch {
		candidates = append(candidates, r.tc.Before) // The head of the layer below, before the push that triggered this run.
	}
	if l.BaseCommit != "" && l.Base == below.Branch {
		candidates = append(candidates, l.BaseCommit) // The head of the layer below, when GitHub last compared the pull request.
	}
	for _, c := range candidates {
		if ok, _ := r.gitTest(ctx, "merge-base", "--is-ancestor", c, l.old); ok {
			return c, nil
		}
	}
	return "", fmt.Errorf("cannot find the commit of #%d (%s) it was built on", below.Number, below.Branch)
}

// restackLayer replays the commits of the layer after upstream onto the parent commit,
// and moves its own migration files on top of the parent's ones. It returns the new head.
func (r *restacker) restackLayer(ctx context.Context, l *layerState, parent, parentName, upstream string) (string, error) {
	commits, err := r.git(ctx, "rev-list", "--count", upstream+".."+l.old)
	if err != nil {
		return "", err
	}
	r.Infof("#%d (%s): replaying %s commit(s) onto %s (%s)", l.Number, l.Branch, commits, parentName, short(parent))
	if _, err := r.git(ctx, "-c", "merge."+sumMergeDriver+".name=atlas-action", "-c", "merge."+sumMergeDriver+".driver=true",
		"rebase", "--quiet", "--onto", parent, upstream, l.old); err != nil {
		files, _ := r.git(ctx, "diff", "--name-only", "--diff-filter=U")
		if _, aerr := r.git(ctx, "rebase", "--abort"); aerr != nil {
			err = errors.Join(err, aerr)
		}
		if files != "" {
			return "", fmt.Errorf("conflicts with %s in %s", parentName, strings.Join(strings.Fields(files), ", "))
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
	if !extendsSum(ps, cs) {
		own, err := r.ownFiles(ctx, parent, parentName, ps, cs)
		if err != nil {
			return "", err
		}
		if err := r.waitClock(ctx, ps, parentName); err != nil {
			return "", err
		}
		if err := r.Atlas.MigrateRebase(ctx, &atlasexec.MigrateRebaseParams{DirURL: r.dirURL, Files: own}); err != nil {
			return "", fmt.Errorf("failed to rebase migrations: %w", err)
		}
		if cs, err = r.sumFile(); err != nil {
			return "", err
		}
		if !extendsSum(ps, cs) {
			return "", fmt.Errorf("its migrations are not on top of the ones of %s after `atlas migrate rebase`", parentName)
		}
		r.Infof("#%d (%s): moved %s on top of %s", l.Number, l.Branch, strings.Join(own, ", "), parentName)
	}
	if _, err := r.git(ctx, "add", "--all", "--", r.dir); err != nil {
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
		// Amend the commit of an earlier run instead of piling up commits.
		if subject, err := r.git(ctx, "log", "-1", "--format=%s", "HEAD"); err == nil && subject == r.message && head != parent {
			args = []string{"commit", "--quiet", "--amend", "--no-edit"}
		}
		if _, err := r.git(ctx, args...); err != nil {
			return "", fmt.Errorf("failed to commit changes: %w", err)
		}
	}
	return r.git(ctx, "rev-parse", "HEAD")
}

// ownFiles returns the migration files the layer adds on top of the parent, and fails if
// the layer edits, removes or duplicates the parent's migration files, as these cannot be
// fixed by moving the layer's files.
func (r *restacker) ownFiles(ctx context.Context, parent, parentName string, ps, cs migrate.HashFile) ([]string, error) {
	pblobs, err := r.blobs(ctx, parent)
	if err != nil {
		return nil, err
	}
	cblobs, err := r.blobs(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	var own, edited, removed, dups []string
	for _, f := range ps {
		switch b, ok := cblobs[f.N]; {
		case !ok:
			removed = append(removed, f.N)
		case b != pblobs[f.N]:
			edited = append(edited, f.N)
		}
	}
	for _, f := range cs {
		if _, ok := pblobs[f.N]; ok {
			continue
		}
		if n := blobName(pblobs, cblobs[f.N]); n != "" {
			dups = append(dups, fmt.Sprintf("%s (same as %s)", f.N, n))
			continue
		}
		own = append(own, f.N)
	}
	switch {
	case len(edited) > 0:
		return nil, fmt.Errorf("it edits migration files of %s: %s", parentName, strings.Join(edited, ", "))
	case len(removed) > 0:
		return nil, fmt.Errorf("it removes migration files of %s: %s", parentName, strings.Join(removed, ", "))
	case len(dups) > 0:
		return nil, fmt.Errorf("it has copies of migration files of %s: %s", parentName, strings.Join(dups, ", "))
	case len(own) == 0:
		return nil, fmt.Errorf("its migration directory does not extend the one of %s", parentName)
	}
	slices.Sort(own)
	return own, nil
}

// blobName returns the name of the file with the given blob, if any.
func blobName(blobs map[string]string, blob string) string {
	for n, b := range blobs {
		if b == blob {
			return n
		}
	}
	return ""
}

// blobs returns the blob of each file in the migration directory at the given commit.
func (r *restacker) blobs(ctx context.Context, c string) (map[string]string, error) {
	out, err := r.gitRaw(ctx, "ls-tree", "-z", "--full-tree", c, "--", path.Dir(r.sumPath)+"/")
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

// waitClock waits for the clock to pass the latest migration version of the parent,
// because `atlas migrate rebase` stamps the files with the current time. Versions
// that are not timestamps are not waited for. The check that the files ended up on
// top of the parent's ones catches any ordering it does not cover.
func (r *restacker) waitClock(ctx context.Context, ps migrate.HashFile, parentName string) error {
	if len(ps) == 0 {
		return nil
	}
	v, ok := fileTimestamp(ps[len(ps)-1].N)
	if !ok {
		return nil
	}
	wait := v.Add(time.Second).Sub(r.now())
	switch {
	case wait <= 0:
		return nil
	case wait > maxClockWait:
		return fmt.Errorf("the latest migration of %s, %s, is versioned %s ahead of the clock", parentName, ps[len(ps)-1].N, wait.Round(time.Second))
	}
	r.Infof("Waiting %s for the clock to pass the latest migration version of %s", wait.Round(time.Millisecond), parentName)
	return r.sleep(ctx, wait)
}

// fileTimestamp returns the version of the migration file as a time, if it is a timestamp.
func fileTimestamp(name string) (time.Time, bool) {
	v := strings.TrimLeft(name, "Vv")
	if i := strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
		v = v[:i]
	}
	if len(v) != len("20060102150405") {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102150405", v)
	return t, err == nil
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
	var (
		leases, refs, names []string
	)
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
	b = fmt.Appendf(b, "/%s merge=%s\n", r.sumPath, sumMergeDriver)
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
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
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
		return false, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return true, nil
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
