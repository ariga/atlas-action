// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas/atlasexec"
	"ariga.io/atlas/sql/migrate"
	"github.com/stretchr/testify/require"
)

const botMessage = "migrations: rebase migration files"

func TestMigrateAutoRebaseStack(t *testing.T) {
	for _, bin := range []string{"atlas", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH, skipping integration test", bin)
		}
	}
	t.Run("TrunkMoved", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.migration("A", "20260102000100", "a2")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.file("B", "app.txt", "B\n")
		r.branch("C", "B")
		r.migration("C", "20260104000000", "c1")
		r.push("A", "B", "C")
		s := r.stack(10, "main", "A", "B", "C")
		before := r.tip("main")
		r.migration("main", "20260105000000", "m1")
		r.push("main")

		act, logs, err := r.run("main", before, s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Empty(t, act.output["latest_version"], "only set for the branch the action runs on")
		r.requireStack("main", "A", "B", "C")
		require.Equal(t, []string{"init", "m1", "a1", "a2"}, r.names("A"))
		require.Equal(t, []string{"init", "m1", "a1", "a2", "b1"}, r.names("B"))
		require.Equal(t, []string{"init", "m1", "a1", "a2", "b1", "c1"}, r.names("C"))
		require.Equal(t, []string{"A: add a1", "A: add a2", botMessage}, r.log("main", "A"))
		require.Equal(t, []string{"B: add b1", "B: app.txt", botMessage}, r.log("A", "B"))
		require.Equal(t, []string{"C: add c1", botMessage}, r.log("B", "C"))
		require.Contains(t, logs, "#1 (A): migrations conflict with main")
		require.Contains(t, logs, "#1 (A): replaying 2 commit(s) onto main")

		// A second run has nothing to do.
		tips := r.tips("A", "B", "C")
		act, logs, err = r.run("main", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B", "C"))
		require.Contains(t, logs, "Migrations of all the pull requests are up to date")
		// Also when it runs on a branch of the stack, as after its own push with a PAT.
		act, logs, err = r.run("B", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B", "C"))

		// The trunk moves again: the commits of the earlier run are amended, not piled up.
		r.migration("main", "20260106000000", "m2")
		r.push("main")
		act, logs, err = r.run("B", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Regexp(t, `^\d{14}$`, act.output["latest_version"])
		vs := r.versions("B")
		require.Equal(t, vs[len(vs)-1], act.output["latest_version"], "the version of b1")
		r.requireStack("main", "A", "B", "C")
		require.Equal(t, []string{"init", "m1", "m2", "a1", "a2", "b1", "c1"}, r.names("C"))
		require.Equal(t, []string{"A: add a1", "A: add a2", botMessage}, r.log("main", "A"))
		require.Equal(t, []string{"B: add b1", "B: app.txt", botMessage}, r.log("A", "B"))
		require.Equal(t, []string{"C: add c1", botMessage}, r.log("B", "C"))
		// The action leaves the branch it ran on checked out, at its new head.
		require.Equal(t, r.tip("B"), r.gitOut(r.ci, "rev-parse", "HEAD"))
		require.Equal(t, "B", r.gitOut(r.ci, "symbolic-ref", "--short", "HEAD"))
		require.Empty(t, r.gitOut(r.ci, "status", "--porcelain"))

		// GitHub lands A with a squash merge, and rebases the layers above it.
		oldA, oldB := r.tip("A"), r.tip("B")
		r.land(s, 0)
		r.rebase("B", "main", oldA)
		r.rebase("C", "B", oldB)
		act, logs, err = r.run("C", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		r.requireStack("main", "B", "C")
	})

	t.Run("LowerLayerPushed", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			branch    string // The branch the action runs on.
			before    bool   // Whether the action knows the previous head of A.
			baseShas  bool   // Whether GitHub reports the previous head of A as the base of B.
			wantError string
		}{
			{name: "PushEvent", branch: "A", before: true},
			{name: "BaseCommit", branch: "C", baseShas: true},
			{name: "Unknown", branch: "C", wantError: "#2 (B): cannot find the commit of #1 (A) it was built on. Nothing was pushed"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.branch("C", "B")
				r.migration("C", "20260104000000", "c1")
				r.push("A", "B", "C")
				s := r.stack(7, "main", "A", "B", "C")
				// The developer amends A, and adds a migration that sorts after the ones above it.
				before := r.tip("A")
				r.amend("A", "A: add a1 (amended)")
				r.migration("A", time.Now().UTC().Format("20060102150405"), "a2")
				r.forcePush("A")
				if !tt.baseShas {
					s.Layers[1].BaseCommit = ""
				}
				var b string
				if tt.before {
					b = before
				}
				tips := r.tips("A", "B", "C")
				act, logs, err := r.run(tt.branch, b, s, nil)
				if tt.wantError != "" {
					require.ErrorContains(t, err, tt.wantError)
					require.Equal(t, "false", act.output["rebased"])
					require.Equal(t, tips, r.tips("A", "B", "C"), "nothing is pushed")
					return
				}
				require.NoError(t, err, logs)
				require.Equal(t, tips[0], r.tip("A"), "A is up to date with main")
				r.requireStack("main", "A", "B", "C")
				require.Equal(t, []string{"init", "a1", "a2", "b1", "c1"}, r.names("C"))
				require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"), "the old commit of A is not replayed")
				require.Equal(t, []string{"C: add c1", botMessage}, r.log("B", "C"))
			})
		}
	})

	t.Run("MergedBelow", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(3, "main", "A", "B")
		// A gets a migration that B was not restacked on, and lands with a squash merge.
		// GitHub cannot rebase B, as their atlas.sum conflict, so it only retargets it.
		r.migration("A", time.Now().UTC().Format("20060102150405"), "a2")
		r.push("A")
		r.land(s, 0)

		act, logs, err := r.run("main", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "B")
		require.Equal(t, []string{"init", "a1", "a2", "b1"}, r.names("B"))
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"), "only the commits of B are replayed")
	})

	t.Run("MigrationsOfOthers", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			change    func(*stackRepo)
			wantError string
		}{
			{
				name: "Conflict",
				change: func(r *stackRepo) {
					r.file("B", "app.txt", "B\n")
					r.file("A", "app.txt", "A\n")
				},
				wantError: "#2 (B): conflicts with #1 (A) in app.txt",
			},
			{
				name: "Edit",
				change: func(r *stackRepo) {
					r.file("B", "migrations/20260102000000_a1.sql", "CREATE TABLE a1 (id int, c int);\n")
					r.hash("B")
				},
				wantError: "#2 (B): it edits migration files of #1 (A)",
			},
			{
				name: "Copy",
				change: func(r *stackRepo) {
					r.file("B", "migrations/20260103000001_copy.sql", "CREATE TABLE a1 (id int);\n")
					r.hash("B")
				},
				wantError: "#2 (B): it has copies of migration files of #1 (A): 20260103000001_copy.sql (same as ",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.file("main", "app.txt", "base\n")
				r.push("main")
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(5, "main", "A", "B")
				// The developer pushes a migration to A, and the change to B is pushed earlier.
				before := r.tip("A")
				tt.change(r)
				r.migration("A", "20260105000000", "a2")
				r.push("A", "B")
				s.Layers[1].Commit = r.tip("B")

				tips := r.tips("A", "B")
				act, logs, err := r.run("A", before, s, nil)
				require.ErrorContains(t, err, tt.wantError, logs)
				require.ErrorContains(t, err, "Nothing was pushed. Restack it locally with `gh stack rebase`")
				require.Equal(t, "false", act.output["rebased"])
				require.Equal(t, tips, r.tips("A", "B"), "nothing is pushed")
				require.Empty(t, r.gitOut(r.ci, "status", "--porcelain"))
				require.Equal(t, "A", r.gitOut(r.ci, "symbolic-ref", "--short", "HEAD"))
				require.Equal(t, tips[0], r.gitOut(r.ci, "rev-parse", "HEAD"))
				// The merge driver is removed from the repository attributes.
				_, err = os.Stat(filepath.Join(r.ci, ".git", "info", "attributes"))
				require.True(t, os.IsNotExist(err))
			})
		}
	})

	t.Run("NoMigrationConflict", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.file("A", "app.txt", "A\n")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(4, "main", "A", "B")
		r.file("main", "other.txt", "main\n")
		r.push("main")

		// The trunk moved, but not its migrations. GitHub can rebase the stack.
		tips := r.tips("A", "B")
		act, logs, err := r.run("main", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B"))
		require.Contains(t, logs, "Migrations of all the pull requests are up to date")

		// The trunk got migrations, but no pull request of the stack adds any of its own.
		r2 := newStackRepo(t)
		r2.branch("A", "main")
		r2.file("A", "app.txt", "A\n")
		r2.push("A")
		s2 := r2.stack(4, "main", "A")
		r2.migration("main", "20260105000000", "m1")
		r2.push("main")
		tips = r2.tips("A")
		act, logs, err = r2.run("main", "", s2, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r2.tips("A"))
		require.Contains(t, logs, "No pull request at or above #1 adds migrations, GitHub can rebase the stack without conflicts")

		// A layer above it does, so both are restacked.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err = r.run("main", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"A: app.txt"}, r.log("main", "A"), "A has no migrations to move")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
		require.Equal(t, []string{"init", "m1", "b1"}, r.names("B"))
	})

	t.Run("Clock", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(2, "main", "A")
		// The trunk got a migration versioned ahead of the clock, as by a restack run.
		ahead := time.Now().UTC().Add(2 * time.Second).Format("20060102150405")
		r.migration("main", ahead, "m1")
		r.push("main")
		act, logs, err := r.run("main", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Contains(t, logs, "Waiting")
		require.Equal(t, []string{"init", "m1", "a1"}, r.names("A"))
		require.Greater(t, r.versions("A")[2], ahead)

		// A version too far ahead is not waited for.
		r.migration("main", time.Now().UTC().Add(time.Hour).Format("20060102150405"), "m2")
		r.push("main")
		tips := r.tips("A")
		_, logs, err = r.run("main", "", s, nil)
		require.ErrorContains(t, err, "ahead of the clock", logs)
		require.Equal(t, tips, r.tips("A"))
	})

	t.Run("Push", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(6, "main", "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// A developer pushes to B while the action restacks the stack.
		var raced bool
		racer := func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name == "git" && len(args) > 0 && args[0] == "push" && !raced {
				raced = true
				r.file("B", "late.txt", "late\n")
				r.push("B")
			}
			return exec.CommandContext(ctx, name, args...)
		}
		tips := r.tips("A", "B")
		act, logs, err := r.run("main", "", s, racer)
		require.True(t, raced)
		require.NoError(t, err, logs)
		require.Contains(t, logs, "Stack #6 changed while it was restacked, nothing was pushed")
		require.Contains(t, logs, "(stale info)")
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips[0], r.tip("A"), "the push is atomic")
		// The next run restacks the branch with the new commit.
		act, logs, err = r.run("B", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"B: add b1", "B: late.txt", botMessage}, r.log("A", "B"))
	})

	t.Run("Stacks", func(t *testing.T) {
		r := newStackRepo(t)
		r.file("main", "app.txt", "base\n")
		r.push("main")
		r.branch("X", "main")
		r.migration("X", "20260102000000", "x1")
		r.branch("Y", "main")
		r.migration("Y", "20260102000000", "y1")
		r.file("Y", "app.txt", "Y\n")
		r.branch("Z", "main")
		r.migration("Z", "20260102000000", "z1")
		r.push("X", "Y", "Z")
		sx := r.stack(11, "main", "X")
		sy := r.stack(12, "main", "Y")
		sz := r.stack(13, "release", "Z") // Targets another branch.
		r.migration("main", "20260105000000", "m1")
		r.file("main", "app.txt", "main\n")
		r.push("main")

		tips := r.tips("X", "Y", "Z")
		scm := &stackSCM{}
		scm.add(sx, sy, sz)
		act, logs, err := r.runSCM("main", "", scm, nil, nil)
		require.ErrorContains(t, err, "stack #12: #1 (Y): conflicts with main in app.txt", logs)
		require.Equal(t, "true", act.output["rebased"], "stack #11 is pushed")
		r.requireStack("main", "X")
		require.Equal(t, tips[1:], r.tips("Y", "Z"))
	})

	t.Run("WorkingDirectory", func(t *testing.T) {
		r := newStackRepo(t, "db/migrations")
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(2, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		scm := &stackSCM{}
		scm.add(s)
		act, logs, err := r.runSCM("A", "", scm, map[string]string{"dir": "file://migrations"}, nil, "db")
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A")
		require.Equal(t, []string{"init", "m1", "a1"}, r.names("A"))
		require.Equal(t, r.versions("A")[2], act.output["latest_version"])
	})

	t.Run("Skipped", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.branch("C", "B")
		r.migration("C", "20260104000000", "c1")
		r.push("A", "B", "C")
		s := r.stack(8, "main", "A", "B", "C")
		s.Layers[1].Open = false // B was closed.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		tips := r.tips("A", "B", "C")
		act, logs, err := r.run("A", "", s, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Contains(t, logs, "pull request #2 is closed, skipping the 1 pull request(s) above it")
		r.requireStack("main", "A")
		require.Equal(t, tips[1:], r.tips("B", "C"))
	})

	t.Run("NotStacked", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.push("A", "B")
		scm := &stackSCM{prs: []*atlasaction.PullRequest{
			{Number: 1, Ref: "A", Base: "main"},
			{Number: 2, Ref: "B", Base: "A"},
		}}
		// A pull request that targets the base branch is rebased as without the input.
		_, logs, err := r.runSCM("A", "", scm, nil, nil)
		require.NoError(t, err, logs)
		require.Contains(t, logs, "No conflict found when merging main into A")
		// One that targets another branch is not.
		act, logs, err := r.runSCM("B", "", scm, nil, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Contains(t, logs, `targets \"A\" and is not stacked, nothing to restack`)
		// Neither is a branch without pull requests.
		act, logs, err = r.runSCM("main", "", scm, nil, nil)
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Contains(t, logs, `has no open pull requests, and no stacks target it, nothing to restack`)
	})

	t.Run("Errors", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.push("A")
		s := r.stack(1, "main", "A")
		scm := &stackSCM{}
		scm.add(s, &atlasaction.Stack{Number: 2, Open: true, Base: "main", Layers: []atlasaction.StackLayer{{Number: 9, Branch: "A", Base: "main", Open: true}}})
		_, _, err := r.runSCM("A", "", scm, nil, nil)
		require.EqualError(t, err, `branch "A" belongs to more than one stack (#1, #2), restack them manually`)

		_, _, err = r.run("A", "", s, nil, map[string]string{"base-sha": "0123456789abcdef"})
		require.EqualError(t, err, `the "stack" input cannot be used with "base-sha" or "force-rebase"`)
		_, _, err = r.run("A", "", s, nil, map[string]string{"force-rebase": "true"})
		require.EqualError(t, err, `the "stack" input cannot be used with "base-sha" or "force-rebase"`)

		act := r.action("A", "", nil, nil)
		act.trigger.SCMClient = func() (atlasaction.SCMClient, error) { return &mockSCM{}, nil }
		require.EqualError(t, r.autorebase(act, nil), `the "stack" input is supported only for repositories hosted on GitHub`)

		// A shallow clone cannot be restacked.
		shallow := filepath.Join(t.TempDir(), "shallow")
		r.git("", "clone", "--quiet", "--depth=1", "file://"+r.origin, shallow)
		r.ci = shallow
		_, _, err = r.run("A", "", s, nil)
		require.ErrorContains(t, err, "restacking requires the full git history, set `fetch-depth: 0` in actions/checkout")
	})
}

type (
	// stackRepo is a git remote with a clone that developers (and GitHub) push
	// from, and a clone that the action runs in, as checked out by actions/checkout.
	stackRepo struct {
		t       *testing.T
		dir     string // Migration directory, relative to the repository root.
		origin  string
		dev, ci string
	}
	// stackSCM is a GitHub client that serves stacks of pull requests.
	stackSCM struct {
		mockSCM
		prs    []*atlasaction.PullRequest
		stacks []*atlasaction.Stack
	}
)

func newStackRepo(t *testing.T, dir ...string) *stackRepo {
	// Isolate the tests from the git configuration of the machine, like commit signing.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	r := &stackRepo{
		t:      t,
		dir:    "migrations",
		origin: filepath.Join(root, "origin.git"),
		dev:    filepath.Join(root, "dev"),
		ci:     filepath.Join(root, "ci"),
	}
	if len(dir) > 0 {
		r.dir = dir[0]
	}
	r.git("", "init", "--quiet", "--bare", "--initial-branch=main", r.origin)
	r.git("", "init", "--quiet", "--initial-branch=main", r.dev)
	r.config(r.dev)
	r.git(r.dev, "remote", "add", "origin", r.origin)
	mdir := filepath.Join(r.dev, r.dir)
	require.NoError(t, os.MkdirAll(mdir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(mdir, "20260101000000_init.sql"), []byte("CREATE TABLE init (id int);\n"), 0644))
	r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
	r.git(r.dev, "add", "--all")
	r.git(r.dev, "commit", "--quiet", "--message", "init")
	r.push("main")
	r.git("", "clone", "--quiet", r.origin, r.ci)
	r.config(r.ci)
	return r
}

func (r *stackRepo) config(dir string) {
	r.git(dir, "config", "user.name", "atlas-action")
	r.git(dir, "config", "user.email", "atlas-action@example.com")
	r.git(dir, "config", "commit.gpgsign", "false")
}

// git runs a git command in the given directory.
func (r *stackRepo) git(dir string, args ...string) {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "git %s: %s", strings.Join(args, " "), out)
}

// gitOut runs a git command in the given directory and returns its trimmed output.
func (r *stackRepo) gitOut(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(r.t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// checkout checks out the local branch in the developer clone.
func (r *stackRepo) checkout(branch string) {
	r.git(r.dev, "checkout", "--quiet", branch)
}

// branch creates a branch in the developer clone.
func (r *stackRepo) branch(name, from string) {
	r.git(r.dev, "branch", name, from)
}

// file writes a file and commits it to the branch.
func (r *stackRepo) file(branch, name, content string) {
	r.checkout(branch)
	p := filepath.Join(r.dev, name)
	require.NoError(r.t, os.MkdirAll(filepath.Dir(p), 0755))
	require.NoError(r.t, os.WriteFile(p, []byte(content), 0644))
	r.git(r.dev, "add", "--all")
	r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("%s: %s", branch, filepath.Base(name)))
}

// migration adds a migration file to the branch, and commits it with the updated atlas.sum.
func (r *stackRepo) migration(branch, version, name string) {
	r.checkout(branch)
	dir := filepath.Join(r.dev, r.dir)
	require.NoError(r.t, os.MkdirAll(dir, 0755))
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, version+"_"+name+".sql"), []byte(fmt.Sprintf("CREATE TABLE %s (id int);\n", name)), 0644))
	r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
	r.git(r.dev, "add", "--all")
	r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("%s: add %s", branch, name))
}

// hash commits the regenerated atlas.sum of the branch.
func (r *stackRepo) hash(branch string) {
	r.checkout(branch)
	r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
	r.git(r.dev, "commit", "--quiet", "--all", "--message", fmt.Sprintf("%s: hash", branch))
}

// amend rewrites the last commit of the branch.
func (r *stackRepo) amend(branch, message string) {
	r.checkout(branch)
	r.git(r.dev, "commit", "--quiet", "--amend", "--message", message)
}

func (r *stackRepo) atlas(dir string, args ...string) {
	r.t.Helper()
	cmd := exec.Command("atlas", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(r.t, err, "atlas %s: %s", strings.Join(args, " "), out)
}

// push pushes the branches of the developer clone.
func (r *stackRepo) push(branches ...string) {
	r.git(r.dev, append([]string{"push", "--quiet", "origin"}, branches...)...)
}

// forcePush force-pushes the branches of the developer clone.
func (r *stackRepo) forcePush(branches ...string) {
	r.git(r.dev, append([]string{"push", "--quiet", "--force", "origin"}, branches...)...)
}

// land lands the bottom layer i of the stack on its base with a squash merge, and
// retargets the layer above it, as GitHub does. It keeps the head of the merged
// layer as the head ref of its pull request.
func (r *stackRepo) land(s *atlasaction.Stack, i int) {
	l := &s.Layers[i]
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.checkout(s.Base)
	r.git(r.dev, "reset", "--quiet", "--hard", "origin/"+s.Base)
	r.git(r.dev, "merge", "--quiet", "--squash", "origin/"+l.Branch)
	r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("Squash %s (#%d)", l.Branch, l.Number))
	r.push(s.Base)
	l.Commit, l.Merged, l.Open = r.tip(l.Branch), true, false
	r.git(r.origin, "update-ref", fmt.Sprintf("refs/pull/%d/head", l.Number), l.Commit)
	if i+1 < len(s.Layers) {
		s.Layers[i+1].Base, s.Layers[i+1].BaseCommit = s.Base, r.tip(s.Base)
	}
}

// rebase rebases the commits of the branch after upstream onto the given branch, and force-pushes it.
func (r *stackRepo) rebase(branch, onto, upstream string) {
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.git(r.dev, "checkout", "--quiet", "-B", branch, "origin/"+branch)
	r.git(r.dev, "rebase", "--quiet", "--onto", "origin/"+onto, upstream)
	r.forcePush(branch)
}

// tip returns the head of the branch on the remote.
func (r *stackRepo) tip(branch string) string {
	return r.gitOut(r.origin, "rev-parse", "refs/heads/"+branch)
}

func (r *stackRepo) tips(branches ...string) []string {
	tips := make([]string, len(branches))
	for i, b := range branches {
		tips[i] = r.tip(b)
	}
	return tips
}

// log returns the subjects of the commits of the remote branch that are not in the base, oldest first.
func (r *stackRepo) log(base, branch string) []string {
	return strings.Split(r.gitOut(r.origin, "log", "--reverse", "--format=%s", base+".."+branch), "\n")
}

// sum returns the atlas.sum of the remote branch.
func (r *stackRepo) sum(branch string) migrate.HashFile {
	r.t.Helper()
	var hf migrate.HashFile
	require.NoError(r.t, hf.UnmarshalText([]byte(r.gitOut(r.origin, "show", branch+":"+r.dir+"/atlas.sum"))))
	return hf
}

// names returns the names of the migration files of the remote branch, without their versions.
func (r *stackRepo) names(branch string) []string {
	var names []string
	for _, f := range r.sum(branch) {
		_, n, _ := strings.Cut(strings.TrimSuffix(f.N, ".sql"), "_")
		names = append(names, n)
	}
	return names
}

// versions returns the versions of the migration files of the remote branch.
func (r *stackRepo) versions(branch string) []string {
	var vs []string
	for _, f := range r.sum(branch) {
		v, _, _ := strings.Cut(f.N, "_")
		vs = append(vs, v)
	}
	return vs
}

// requireStack checks that each branch contains the one below it, that its migrations
// are on top of the ones below it, and that its migration directory is valid.
func (r *stackRepo) requireStack(base string, branches ...string) {
	r.t.Helper()
	for i, b := range branches {
		parent := base
		if i > 0 {
			parent = branches[i-1]
		}
		r.git(r.origin, "merge-base", "--is-ancestor", parent, b)
		ps, cs := r.sum(parent), r.sum(b)
		require.GreaterOrEqual(r.t, len(cs), len(ps), "%s keeps the migrations of %s", b, parent)
		require.Equal(r.t, ps, cs[:len(ps)], "%s extends the migrations of %s", b, parent)
		dir := filepath.Join(r.t.TempDir(), b)
		r.git("", "clone", "--quiet", "--branch", b, r.origin, dir)
		r.atlas(dir, "migrate", "validate", "--dir", "file://"+r.dir)
	}
}

// stack returns a stack of the given branches, from the bottom up, as GitHub reports it.
func (r *stackRepo) stack(n int, base string, branches ...string) *atlasaction.Stack {
	s := &atlasaction.Stack{Number: n, Open: true, Base: base}
	for i, b := range branches {
		l := atlasaction.StackLayer{Number: i + 1, Branch: b, Commit: r.tip(b), Base: base, Open: true}
		if i > 0 {
			l.Base = branches[i-1]
		}
		if !strings.HasPrefix(l.Base, "release") {
			l.BaseCommit = r.tip(l.Base)
		}
		// GitHub keeps the head of the pull request as refs/pull/<number>/head.
		r.git(r.origin, "update-ref", fmt.Sprintf("refs/pull/%d/head", l.Number), l.Commit)
		s.Layers = append(s.Layers, l)
	}
	return s
}

func (r *stackRepo) run(branch, before string, s *atlasaction.Stack, exec func(context.Context, string, ...string) *exec.Cmd, inputs ...map[string]string) (*mockAction, string, error) {
	scm := &stackSCM{}
	scm.add(s)
	var in map[string]string
	if len(inputs) > 0 {
		in = inputs[0]
	}
	return r.runSCM(branch, before, scm, in, exec)
}

// runSCM runs the action on a push to the branch, in the working directory relative to the clone.
func (r *stackRepo) runSCM(branch, before string, scm *stackSCM, inputs map[string]string, exec func(context.Context, string, ...string) *exec.Cmd, wd ...string) (*mockAction, string, error) {
	r.t.Helper()
	// Check out the branch, as actions/checkout does with fetch-depth: 0.
	r.git(r.ci, "fetch", "--quiet", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*")
	r.git(r.ci, "checkout", "--quiet", "--force", "-B", branch, "origin/"+branch)
	if len(wd) > 0 {
		r.t.Chdir(filepath.Join(r.ci, wd[0]))
	} else {
		r.t.Chdir(r.ci)
	}
	act := r.action(branch, before, scm, inputs)
	var logs bytes.Buffer
	act.logger = slog.New(slog.NewTextHandler(&logs, nil))
	err := r.autorebase(act, exec)
	return act, logs.String(), err
}

func (r *stackRepo) action(branch, before string, scm *stackSCM, inputs map[string]string) *mockAction {
	in := map[string]string{"stack": "true", "dir": "file://" + r.dir}
	for k, v := range inputs {
		in[k] = v
	}
	return &mockAction{
		inputs: in,
		trigger: &atlasaction.TriggerContext{
			SCMType:       atlasexec.SCMTypeGithub,
			Branch:        branch,
			DefaultBranch: "main",
			Before:        before,
			SCMClient:     func() (atlasaction.SCMClient, error) { return scm, nil },
		},
	}
}

func (r *stackRepo) autorebase(act *mockAction, run func(context.Context, string, ...string) *exec.Cmd) error {
	r.t.Helper()
	cli, err := atlasexec.NewClient("", "atlas")
	require.NoError(r.t, err)
	if run == nil {
		run = exec.CommandContext
	}
	acts, err := atlasaction.New(
		atlasaction.WithAction(act),
		atlasaction.WithAtlas(cli),
		atlasaction.WithCmdExecutor(run),
	)
	require.NoError(r.t, err)
	return acts.MigrateAutoRebase(context.Background())
}

// add adds the stacks, and their open pull requests.
func (s *stackSCM) add(stacks ...*atlasaction.Stack) {
	for _, st := range stacks {
		s.stacks = append(s.stacks, st)
	}
}

// OpenPullRequests implements atlasaction.StackClient.
func (s *stackSCM) OpenPullRequests(_ context.Context, head, base string) ([]*atlasaction.PullRequest, error) {
	prs := slices.Clone(s.prs)
	for _, st := range s.stacks {
		for _, l := range st.Layers {
			if l.Open {
				prs = append(prs, &atlasaction.PullRequest{Number: l.Number, Ref: l.Branch, Base: l.Base, Stack: st.Number})
			}
		}
	}
	return slices.DeleteFunc(prs, func(pr *atlasaction.PullRequest) bool {
		return head != "" && pr.Ref != head || base != "" && pr.Base != base
	}), nil
}

// Stack implements atlasaction.StackClient.
func (s *stackSCM) Stack(_ context.Context, n int) (*atlasaction.Stack, error) {
	for _, st := range s.stacks {
		if st.Number == n {
			return st, nil
		}
	}
	return nil, fmt.Errorf("stack #%d not found", n)
}

var _ atlasaction.StackClient = (*stackSCM)(nil)

func TestGitHubTriggerContextBefore(t *testing.T) {
	event := filepath.Join(t.TempDir(), "event.json")
	for _, tt := range []struct {
		name, event, payload, want string
	}{
		{name: "Push", event: "push", payload: `{"before":"35f82a7a3a0aee6352956853ca57756256499558","forced":true}`, want: "35f82a7a3a0aee6352956853ca57756256499558"},
		{name: "NewBranch", event: "push", payload: `{"before":"0000000000000000000000000000000000000000"}`},
		{name: "PullRequest", event: "pull_request", payload: `{"before":"35f82a7a3a0aee6352956853ca57756256499558","pull_request":{"number":1}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(event, []byte(tt.payload), 0644))
			env := map[string]string{
				"GITHUB_EVENT_PATH": event,
				"GITHUB_EVENT_NAME": tt.event,
				"GITHUB_REF_NAME":   "A",
				"GITHUB_REPOSITORY": "owner/repo",
			}
			tc, err := atlasaction.NewGitHub(func(k string) string { return env[k] }, io.Discard).GetTriggerContext(context.Background())
			require.NoError(t, err)
			require.Equal(t, "A", tc.Branch)
			require.Equal(t, tt.want, tc.Before)
		})
	}
}
