// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

		act, logs, err := r.run(s, run{branch: "main", before: before})
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
		require.Regexp(t, `#1 \(A\): renamed 20260102000000_a1.sql to \d{14}_a1.sql, 20260102000100_a2.sql to \d{14}_a2.sql, to place them on top of main`, logs)

		// A second run has nothing to do, and does not read the history of the branches.
		tips := r.tips("A", "B", "C")
		var scm *stackSCM
		act, logs, err = r.run(s, run{branch: "main", scm: func(s *stackSCM) atlasaction.SCMClient { scm = s; return s }})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B", "C"))
		require.Contains(t, logs, "Migrations of all the pull requests are up to date")
		require.Zero(t, scm.reads)
		// Also when it runs on a branch of the stack, as after its own push with a PAT.
		act, logs, err = r.run(s, run{branch: "B"})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B", "C"))

		// The trunk moves again, and the commits of the earlier run are amended.
		r.migration("main", "20260106000000", "m2")
		r.push("main")
		act, logs, err = r.run(s, run{branch: "B"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
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
		act, logs, err = r.run(s, run{branch: "C"})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		r.requireStack("main", "B", "C")
	})

	t.Run("LowerLayerPushed", func(t *testing.T) {
		for _, tt := range []struct {
			name       string
			branch     string // The branch the action runs on.
			before     bool   // Whether the action knows the previous head of A from the push.
			base       bool   // Whether GitHub reports the previous head of A as the base of B.
			history    bool   // Whether the history of A is known.
			noTimeline bool   // Whether only the activity of the branch of A is known.
			wantError  string
		}{
			{name: "PushEvent", branch: "A", before: true, history: true},
			{name: "History", branch: "C", history: true},
			// The force-pushes of A cannot be read, but the activity of its branch can.
			{name: "Activity", branch: "C", history: true, noTimeline: true},
			// Without the history, the heads A is known to have had cannot tell whether
			// it dropped commits that B has, so the fork point is not trusted.
			{name: "NoHistory", branch: "A", before: true, base: true, wantError: "#2 (B): cannot find the commit of #1 (A) it was built on. Nothing was pushed"},
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
				if !tt.base {
					s.Layers[1].BaseCommit = ""
				}
				o := run{branch: tt.branch, noHistory: !tt.history, noTimeline: tt.noTimeline}
				if tt.before {
					o.before = before
				}
				tips := r.tips("A", "B", "C")
				act, logs, err := r.run(s, o)
				if tt.wantError != "" {
					require.ErrorContains(t, err, tt.wantError)
					require.Contains(t, logs, "failed to get the force-pushes of the pull requests")
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

	t.Run("StalePushEvent", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(12, "main", "A", "B")
		// A gets a migration that is later dropped, and B is rebuilt on top of it.
		a1 := r.tip("A")
		r.migration("A", "20260102000100", "bad")
		r.push("A")
		a2 := r.tip("A")
		r.git(r.dev, "branch", "--force", "B", a2)
		r.migration("B", "20260103000000", "b1")
		r.forcePush("B")
		s.Layers[1].BaseCommit = a2
		// A drops it, gets another migration, and is force-pushed.
		r.checkout("A")
		r.git(r.dev, "reset", "--quiet", "--hard", a1)
		r.migration("A", time.Now().UTC().Format("20060102150405"), "a2")
		r.forcePush("A")
		// The run of the first push to A runs only now.
		_, logs, err := r.run(s, run{branch: "A", before: a1, commit: a2})
		require.NoError(t, err, logs)
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"init", "a1", "a2", "b1"}, r.names("B"), "the dropped migration is not brought back")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
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

		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "B")
		require.Equal(t, []string{"init", "a1", "a2", "b1"}, r.names("B"))
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"), "only the commits of B are replayed")
	})

	t.Run("RewrittenBelowAndMerged", func(t *testing.T) {
		for _, history := range []bool{true, false} {
			t.Run(fmt.Sprint("History=", history), func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(4, "main", "A", "B")
				// A is rewritten: a1 is replaced by a newer version with other contents. It lands
				// before B is restacked, so B still has the old a1, and GitHub cannot rebase it.
				r.checkout("A")
				r.git(r.dev, "reset", "--quiet", "--hard", "main")
				r.migrationSQL("A", time.Now().UTC().Format("20060102150405"), "a1", "CREATE TABLE a1 (id bigint);\n")
				r.forcePush("A")
				r.land(s, 0)

				tips := r.tips("B")
				act, logs, err := r.run(s, run{branch: "main", noHistory: !history})
				if !history {
					require.ErrorContains(t, err, "#2 (B): cannot find the commit of the merged #1 (A) it was built on. Nothing was pushed", logs)
					require.Equal(t, tips, r.tips("B"))
					return
				}
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"])
				r.requireStack("main", "B")
				require.Equal(t, []string{"init", "a1", "b1"}, r.names("B"), "the old a1 is not brought back")
				require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"))
			})
		}
	})

	t.Run("MergeCommitBelow", func(t *testing.T) {
		r := newStackRepo(t)
		r.file("main", "app.txt", "v1\n")
		r.push("main")
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(5, "main", "A", "B")
		// A lands with a merge commit, so its head is in the trunk.
		r.land(s, 0, "merge")
		r.file("main", "app.txt", "v2\n")
		r.migration("main", "20260105000000", "m2")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "B")
		// The trunk reverts its change, and moves again.
		r.file("main", "app.txt", "v1\n")
		r.migration("main", "20260106000000", "m3")
		r.push("main")
		_, logs, err = r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "B")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"), "no commit of the trunk is replayed")
		require.Equal(t, "v1", r.gitOut(r.origin, "show", "B:app.txt"))
	})

	t.Run("CherryPicked", func(t *testing.T) {
		r := newStackRepo(t)
		r.file("main", "app.txt", "v1\n")
		r.push("main")
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.file("A", "app.txt", "v2\n")
		r.push("A")
		s := r.stack(6, "main", "A")
		// The fix of A is cherry-picked to the trunk, that changes it again.
		r.checkout("main")
		r.git(r.dev, "cherry-pick", r.tip("A"))
		r.file("main", "app.txt", "v3\n")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A")
		require.Equal(t, []string{"A: add a1", botMessage}, r.log("main", "A"))
		require.Equal(t, "v3", r.gitOut(r.origin, "show", "A:app.txt"))
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
				// A copy the pull request made itself is its own migration.
				name: "Copy",
				change: func(r *stackRepo) {
					r.file("B", "migrations/20260103000001_copy.sql", "CREATE TABLE a1 (id int);\n")
					r.hash("B")
				},
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
				act, logs, err := r.run(s, run{branch: "A", before: before})
				if tt.wantError == "" {
					require.NoError(t, err, logs)
					require.Equal(t, "true", act.output["rebased"])
					r.requireStack("main", "A", "B")
					return
				}
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
		act, logs, err := r.run(s, run{branch: "main"})
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
		act, logs, err = r2.run(s2, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r2.tips("A"))
		require.Contains(t, logs, "No pull request at or above #1 adds migrations, GitHub can rebase the stack without conflicts")

		// A layer above it does, so both are restacked.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err = r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"A: app.txt"}, r.log("main", "A"), "A has no migrations to move")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
		require.Equal(t, []string{"init", "m1", "b1"}, r.names("B"))
	})

	t.Run("NoMigrationsOnTrunk", func(t *testing.T) {
		r := newStackRepo(t)
		r.checkout("main")
		r.git(r.dev, "rm", "--quiet", "-r", r.dir)
		r.git(r.dev, "commit", "--quiet", "--message", "main: no migrations")
		r.push("main")
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(9, "main", "A", "B")
		// The trunk moves, and still has no migrations.
		r.file("main", "app.txt", "main\n")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Contains(t, logs, "Migrations of all the pull requests are up to date")
		// The first migration lands on the trunk, while the stack adds the first ones too.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err = r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"m1", "a1", "b1"}, r.names("B"))
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
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Contains(t, logs, "Waiting")
		require.Equal(t, []string{"init", "m1", "a1"}, r.names("A"))
		require.Greater(t, r.versions("A")[2], ahead)

		// A version far ahead of the clock that the run did not write is not waited for.
		r.migration("main", time.Now().UTC().Add(time.Hour).Format("20060102150405"), "m2")
		r.push("main")
		tips := r.tips("A")
		_, logs, err = r.run(s, run{branch: "main"})
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
		act, logs, err := r.run(s, run{branch: "main", exec: racer})
		require.True(t, raced)
		require.NoError(t, err, logs)
		require.Contains(t, logs, "Stack #6 changed while it was restacked, nothing was pushed")
		require.Contains(t, logs, "(stale info)")
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips[0], r.tip("A"), "the push is atomic")
		// The next run restacks the branch with the new commit.
		act, logs, err = r.run(s, run{branch: "B"})
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
		act, logs, err := r.run(nil, run{branch: "main", stacks: []*atlasaction.Stack{sx, sy, sz}})
		require.ErrorContains(t, err, "stack #12: #1 (Y): conflicts with main in app.txt", logs)
		require.Equal(t, "true", act.output["rebased"], "stack #11 is pushed")
		r.requireStack("main", "X")
		require.Equal(t, tips[1:], r.tips("Y", "Z"))
	})

	t.Run("TrunkWithPullRequest", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("develop", "main")
		r.file("develop", "develop.txt", "develop\n")
		r.branch("A", "develop")
		r.migration("A", "20260102000000", "a1")
		r.push("develop", "A")
		s := r.stack(14, "develop", "A")
		r.migration("develop", "20260105000000", "d1")
		r.push("develop")
		// develop has its own pull request, which is not stacked.
		act, logs, err := r.run(s, run{branch: "develop", prs: []*atlasaction.PullRequest{{Number: 99, Ref: "develop", Base: "main"}}})
		require.NoError(t, err, logs)
		require.Contains(t, logs, "No conflict found when merging main into develop", "develop is rebased onto main")
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("develop", "A")
	})

	t.Run("TrunkRewritten", func(t *testing.T) {
		// develop has no pull request, and its commit adds two files.
		r := newStackRepo(t)
		r.branch("develop", "main")
		r.file("develop", "x.txt", "x\n")
		r.file("develop", "y.txt", "y\n")
		r.git(r.dev, "reset", "--quiet", "--soft", "main")
		r.git(r.dev, "commit", "--quiet", "--message", "develop: add x and y")
		r.branch("A", "develop")
		r.migration("A", "20260102000000", "a1")
		r.push("develop", "A")
		s := r.stack(15, "develop", "A")
		// develop is rewritten: its commit no longer adds y.txt, and it gets a migration.
		before := r.tip("develop")
		r.checkout("develop")
		r.git(r.dev, "reset", "--quiet", "--hard", "main")
		r.file("develop", "x.txt", "x\n")
		r.migration("develop", time.Now().UTC().Format("20060102150405"), "d1")
		r.forcePush("develop")

		act, logs, err := r.run(s, run{branch: "develop", before: before})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("develop", "A")
		require.Equal(t, []string{"A: add a1", botMessage}, r.log("develop", "A"), "the old commit of develop is not replayed")
		require.NotContains(t, strings.Fields(r.gitOut(r.origin, "ls-tree", "--name-only", "A")), "y.txt")
	})

	t.Run("NotStacked", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.push("A", "B")
		prs := []*atlasaction.PullRequest{
			{Number: 1, Ref: "A", Base: "main"},
			{Number: 2, Ref: "B", Base: "A"},
		}
		// A pull request that targets the base branch is rebased as without the input.
		_, logs, err := r.run(nil, run{branch: "A", prs: prs})
		require.NoError(t, err, logs)
		require.Contains(t, logs, "No conflict found when merging main into A")
		// One that targets another branch is not.
		act, logs, err := r.run(nil, run{branch: "B", prs: prs})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Contains(t, logs, `targets \"A\" and is not stacked`)
		require.Contains(t, logs, `No stacks target \"B\", nothing to restack`)
		// Neither is a branch without pull requests.
		act, logs, err = r.run(nil, run{branch: "main", prs: prs})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Contains(t, logs, `No stacks target \"main\", nothing to restack`)
	})

	t.Run("Layouts", func(t *testing.T) {
		for _, tt := range []struct {
			name  string
			dir   string // Migration directory, relative to the repository root.
			setup func(*stackRepo)
			run   run
			check func(*testing.T, *stackRepo, *mockAction)
		}{
			{
				name: "WorkingDirectory",
				dir:  "db/migrations",
				run:  run{branch: "A", inputs: map[string]string{"dir": "file://migrations"}, wd: "db"},
				check: func(t *testing.T, r *stackRepo, act *mockAction) {
					require.Equal(t, r.versions("A")[2], act.output["latest_version"])
				},
			},
			{
				name: "RepositoryRoot",
				dir:  ".",
				setup: func(r *stackRepo) {
					// The shim downloads the atlas-action binary to the working directory.
					require.NoError(t, os.WriteFile(filepath.Join(r.ci, "atlas-action"), []byte("binary"), 0755))
				},
				run: run{branch: "main", inputs: map[string]string{"dir": "file://."}},
				check: func(t *testing.T, r *stackRepo, _ *mockAction) {
					_, err := os.Stat(filepath.Join(r.ci, "atlas-action"))
					require.NoError(t, err, "files that git does not track are kept")
					require.NotContains(t, r.gitOut(r.origin, "ls-tree", "--name-only", "A"), "atlas-action")
				},
			},
			{
				name: "FullRef",
				run:  run{branch: "refs/heads/main"},
			},
			{
				name: "SymlinkedWorkingDirectory",
				setup: func(r *stackRepo) {
					link := filepath.Join(t.TempDir(), "link")
					require.NoError(t, os.Symlink(r.ci, link))
					r.ciLink = link
				},
				run: run{branch: "main"},
			},
			{
				name: "SumIgnore",
				setup: func(r *stackRepo) {
					r.checkout("A")
					r.migrationSQL("A", "20260102000001", "seed", "-- atlas:sum ignore\nINSERT INTO init VALUES (1);\n")
					r.push("A")
				},
				run: run{branch: "main"},
				check: func(t *testing.T, r *stackRepo, _ *mockAction) {
					files := r.gitOut(r.origin, "ls-tree", "--name-only", "A", "migrations/")
					require.Regexp(t, `migrations/20260105000000_m1.sql\nmigrations/\d{14}_a1.sql\nmigrations/\d{14}_seed.sql`, files)
				},
			},
			{
				name: "EmptyMigration",
				setup: func(r *stackRepo) {
					r.file("main", "migrations/.gitkeep", "")
					r.push("main")
					r.migrationSQL("A", "20260102000001", "draft", "")
					r.push("A")
				},
				run: run{branch: "main"},
			},
			{
				name: "TagLikeBranch",
				setup: func(r *stackRepo) {
					r.git(r.dev, "tag", "main", "main")
					r.git(r.dev, "push", "--quiet", "origin", "refs/tags/main")
				},
				run: run{branch: "main"},
				check: func(t *testing.T, r *stackRepo, _ *mockAction) {
					require.Equal(t, "refs/heads/main", r.gitOut(r.ci, "symbolic-ref", "HEAD"))
				},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t, tt.dir)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.push("A")
				if tt.setup != nil {
					tt.setup(r)
				}
				s := r.stack(2, "main", "A")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				act, logs, err := r.run(s, tt.run)
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"])
				r.requireStack("main", "A")
				if tt.check != nil {
					tt.check(t, r, act)
				}
			})
		}
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
		act, logs, err := r.run(s, run{branch: "A"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Contains(t, logs, "pull request #2 is closed, skipping the 1 pull request(s) above it")
		r.requireStack("main", "A")
		require.Equal(t, tips[1:], r.tips("B", "C"))
	})

	t.Run("RewrittenBeforeOpened", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			rewrite   func(*stackRepo)
			want      []string // Migrations of B with the activity of A.
			wantError string   // Regular expression of the error when the activity of A does not go back that far.
		}{
			{
				// a1 is amended in place, as in `git commit --amend`.
				name: "Amended",
				rewrite: func(r *stackRepo) {
					r.checkout("A")
					r.git(r.dev, "reset", "--quiet", "--hard", "main")
					r.migrationSQL("A", "20260102000000", "a1", "CREATE TABLE a1 (id bigint);\n")
				},
				want:      []string{"init", "m1", "a1", "b1"},
				wantError: `#2 \(B\): cannot tell its commits from the ones of #1 \(A\), as both add 20260102000000_a1\.sql with different contents\. Nothing was pushed`,
			},
			{
				// A is rebased onto the trunk, and a1 is moved on top of the trunk's migrations.
				name: "Renamed",
				rewrite: func(r *stackRepo) {
					r.migration("main", "20260101000100", "m0")
					r.push("main")
					r.checkout("A")
					r.git(r.dev, "reset", "--quiet", "--hard", "main")
					r.migration("A", "20260102000100", "a1")
				},
				want:      []string{"init", "m0", "m1", "a1", "b1"},
				wantError: `#2 \(B\): it has copies of migration files of #1 \(A\): 20260102000000_a1\.sql \(same as \d{14}_a1\.sql\)\. Nothing was pushed`,
			},
			{
				// A drops a migration that B was built on. Only the activity of A tells.
				name: "Rewound",
				rewrite: func(r *stackRepo) {
					r.checkout("A")
					r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
				},
				want: []string{"init", "m1", "a1", "b1"},
			},
		} {
			for _, activity := range []string{"", "partial"} {
				if activity != "" && tt.wantError == "" {
					continue
				}
				t.Run(tt.name+"/Activity="+cmp.Or(activity, "full"), func(t *testing.T) {
					r := newStackRepo(t)
					r.branch("A", "main")
					r.migration("A", "20260102000000", "a1")
					if tt.name == "Rewound" {
						r.migration("A", "20260102000100", "bad")
					}
					r.branch("B", "A")
					r.migration("B", "20260103000000", "b1")
					r.push("A", "B")
					// A is rewritten before the pull requests are opened, which only the activity of its branch records.
					tt.rewrite(r)
					r.forcePush("A")
					s := r.stack(15, "main", "A", "B")
					r.migration("main", "20260105000000", "m1")
					r.push("main")
					tips := r.tips("A", "B")
					act, logs, err := r.run(s, run{branch: "main", activity: activity})
					if activity == "partial" {
						require.Error(t, err, logs)
						require.Regexp(t, tt.wantError, err.Error())
						require.Contains(t, logs, `The activity of branch \"A\" does not go back to its creation, using the force-pushes of #1`)
						require.Equal(t, "false", act.output["rebased"])
						require.Equal(t, tips, r.tips("A", "B"), "nothing is pushed")
						return
					}
					require.NoError(t, err, logs)
					require.Equal(t, "true", act.output["rebased"])
					r.requireStack("main", "A", "B")
					require.Equal(t, tt.want, r.names("B"))
					require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"), "the old commits of A are not replayed")
				})
			}
		}
	})

	t.Run("ActivityNotAvailable", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.branch("C", "B")
		r.migration("C", "20260104000000", "c1")
		r.push("A", "B", "C")
		s := r.stack(22, "main", "A", "B", "C")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// The force-pushes of the pull requests are used, and the activity is tried only once.
		_, logs, err := r.run(s, run{branch: "main", activity: "none"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A", "B", "C")
		require.Equal(t, 1, strings.Count(logs, "failed to read the activity of branch"), logs)
	})

	t.Run("RewoundBelow", func(t *testing.T) {
		for _, history := range []bool{true, false} {
			t.Run(fmt.Sprint("History=", history), func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.migration("A", "20260102000100", "bad")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(16, "main", "A", "B")
				// A drops its last commit, and B is not restacked. GitHub compares B with the new head of A.
				r.checkout("A")
				r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
				r.forcePush("A")
				s.Layers[1].BaseCommit = r.tip("A")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("A", "B")
				act, logs, err := r.run(s, run{branch: "main", noHistory: !history})
				if !history {
					require.ErrorContains(t, err, "#2 (B): cannot find the commit of #1 (A) it was built on. Nothing was pushed", logs)
					require.Equal(t, "false", act.output["rebased"])
					require.Equal(t, tips, r.tips("A", "B"))
					return
				}
				require.NoError(t, err, logs)
				r.requireStack("main", "A", "B")
				require.Equal(t, []string{"init", "m1", "a1", "b1"}, r.names("B"), "the dropped migration is not brought back")
			})
		}
	})

	t.Run("UnreadableHistory", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.branch("C", "B")
		r.migration("C", "20260104000000", "c1")
		r.branch("D", "C")
		r.migration("D", "20260104000100", "d1")
		r.push("A", "B", "C", "D")
		s := r.stack(7, "main", "A", "B", "C", "D")
		// A is amended after B was built on it, and lands.
		r.amend("A", "A: add a1 (amended)")
		r.forcePush("A")
		r.land(s, 0)
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// The history of #3 cannot be read, which stops only the restack that needs it.
		tips := r.tips("B", "C", "D")
		_, logs, err := r.run(s, run{branch: "main", unreadable: []int{3}})
		require.ErrorContains(t, err, "stack #7: #4 (D): cannot find the commit of #3 (C) it was built on. Nothing was pushed", logs)
		require.NotContains(t, err.Error(), "#2 (B)")
		require.Contains(t, logs, "failed to get the force-pushes of pull request #3")
		require.Equal(t, tips, r.tips("B", "C", "D"))
		// Once it can be read, the stack is restacked from the history of the merged #1.
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "B", "C", "D")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"))
	})

	t.Run("SameVersion", func(t *testing.T) {
		// Y is restacked in the same second as X, which then lands.
		r := newStackRepo(t)
		r.branch("Y", "main")
		r.migration("Y", "20260105000000", "y1")
		r.push("Y")
		s := r.stack(12, "main", "Y")
		r.migration("main", "20260105000000", "x1")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "Y")
		require.Equal(t, []string{"init", "x1", "y1"}, r.names("Y"))
		require.Greater(t, r.versions("Y")[2], "20260105000000")

		// A branch that already has the trunk's migration, and its own of the same version.
		r2 := newStackRepo(t)
		r2.migration("main", "20260105000000", "x1")
		r2.push("main")
		r2.branch("Y", "main")
		r2.migration("Y", "20260105000000", "y1")
		r2.push("Y")
		s2 := r2.stack(12, "main", "Y")
		act, logs, err = r2.run(s2, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Contains(t, logs, "#1 (Y): migrations conflict with main")
		r2.requireStack("main", "Y")
	})

	t.Run("MergeInLayer", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			own       bool   // Whether the merge commit has changes of its own.
			wantError string // Regular expression.
		}{
			{name: "Clean"},
			{name: "OwnChanges", own: true, wantError: `#1 \(A\): merge commit [0-9a-f]{7} has changes of its own in fix\.txt, which a restack drops\. Nothing was pushed`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.file("main", "app.txt", "v1\n")
				r.push("main")
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.push("A")
				s := r.stack(17, "main", "A")
				r.file("main", "app.txt", "v2\n")
				r.push("main")
				// The developer merges the trunk into A.
				r.checkout("A")
				r.git(r.dev, "merge", "--quiet", "--no-ff", "--no-commit", "main")
				if tt.own {
					require.NoError(t, os.WriteFile(filepath.Join(r.dev, "fix.txt"), []byte("fix\n"), 0644))
					r.git(r.dev, "add", "fix.txt")
				}
				r.git(r.dev, "commit", "--quiet", "--message", "Merge main into A")
				r.push("A")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("A")
				act, logs, err := r.run(s, run{branch: "main"})
				if tt.wantError != "" {
					require.Error(t, err, logs)
					require.Regexp(t, tt.wantError, err.Error())
					require.Equal(t, "false", act.output["rebased"])
					require.Equal(t, tips, r.tips("A"))
					return
				}
				require.NoError(t, err, logs)
				r.requireStack("main", "A")
				require.Equal(t, "v2", r.gitOut(r.origin, "show", "A:app.txt"))
			})
		}
	})

	t.Run("TrunkWithPullRequestConflict", func(t *testing.T) {
		r := newStackRepo(t)
		r.file("main", "app.txt", "base\n")
		r.push("main")
		r.branch("develop", "main")
		r.file("develop", "app.txt", "develop\n")
		r.branch("A", "develop")
		r.migration("A", "20260102000000", "a1")
		r.push("develop", "A")
		s := r.stack(14, "develop", "A")
		r.file("main", "app.txt", "main\n")
		r.push("main")
		r.migration("develop", "20260105000000", "d1")
		r.push("develop")
		// The pull request of develop conflicts with main in other files, which does not stop the stack.
		act, logs, err := r.run(s, run{branch: "develop", prs: []*atlasaction.PullRequest{{Number: 99, Ref: "develop", Base: "main"}}})
		require.ErrorContains(t, err, "conflict found in files other than migrations/atlas.sum", logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("develop", "A")
		require.Empty(t, r.gitOut(r.ci, "status", "--porcelain"))
		require.Equal(t, "develop", r.gitOut(r.ci, "symbolic-ref", "--short", "HEAD"))
	})

	t.Run("ReAdded", func(t *testing.T) {
		// A pull request adds a migration with the contents of an earlier one.
		r := newStackRepo(t)
		r.migrationSQL("main", "20260101000100", "add_email", "ALTER TABLE init ADD COLUMN email text;\n")
		r.migrationSQL("main", "20260101000200", "drop_email", "ALTER TABLE init DROP COLUMN email;\n")
		r.push("main")
		r.branch("A", "main")
		r.migrationSQL("A", "20260102000000", "readd_email", "ALTER TABLE init ADD COLUMN email text;\n")
		r.push("A")
		s := r.stack(18, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A")
		require.Equal(t, []string{"init", "add_email", "drop_email", "m1", "readd_email"}, r.names("A"))

		// Empty drafts have the same contents.
		r2 := newStackRepo(t)
		r2.branch("A", "main")
		r2.migrationSQL("A", "20260102000000", "draft_a", "")
		r2.branch("B", "A")
		r2.migrationSQL("B", "20260103000000", "draft_b", "")
		r2.push("A", "B")
		s2 := r2.stack(18, "main", "A", "B")
		r2.migration("main", "20260105000000", "m1")
		r2.push("main")
		_, logs, err = r2.run(s2, run{branch: "main"})
		require.NoError(t, err, logs)
		r2.requireStack("main", "A", "B")
	})

	t.Run("EditsBelow", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.file("B", "migrations/20260102000000_a1.sql", "CREATE TABLE a1 (id int, c int);\n")
		r.hash("B")
		r.push("A", "B")
		s := r.stack(19, "main", "A", "B")
		tips := r.tips("A", "B")
		// Nothing moved, so the stack does not need a restack.
		for _, branch := range []string{"B", "A"} {
			act, logs, err := r.run(s, run{branch: branch})
			require.NoError(t, err, logs)
			require.Equal(t, "false", act.output["rebased"])
			require.Contains(t, logs, "Migrations of all the pull requests are up to date")
		}
		r.file("main", "other.txt", "main\n")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B"))
		// The trunk gets a migration, and B cannot be restacked without its edit of a1.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err = r.run(s, run{branch: "main"})
		require.ErrorContains(t, err, "#2 (B): ", logs)
		require.ErrorContains(t, err, "Nothing was pushed")
		require.Equal(t, tips[1], r.tip("B"))
	})

	t.Run("LookupErrors", func(t *testing.T) {
		r := newStackRepo(t)
		for _, b := range []string{"X", "Y", "Z"} {
			r.branch(b, "main")
			r.migration(b, "20260102000000", strings.ToLower(b)+"1")
		}
		r.push("X", "Y", "Z")
		stacks := []*atlasaction.Stack{
			r.stackAt(11, 1, "main", "X"),
			r.stackAt(12, 2, "main", "Y"),
			r.stackAt(13, 3, "main", "Z"),
		}
		// #12 is unstacked after the pull requests were listed.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		tips := r.tips("Y")
		act, logs, err := r.run(nil, run{branch: "main", stacks: stacks, unstacked: []int{12}})
		require.NoError(t, err, logs)
		require.Contains(t, logs, "Stack #12 of pull request #2 was not found, skipping it")
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "X")
		r.requireStack("main", "Z")
		require.Equal(t, tips, r.tips("Y"))
		// Stacks restacked in one run get different versions, as either may land first.
		require.Greater(t, r.versions("Z")[2], r.versions("X")[2])
		// The lookup of #12 fails, and the other stacks are still restacked.
		r.migration("main", "20260106000000", "m2")
		r.push("main")
		act, logs, err = r.run(nil, run{branch: "main", stacks: stacks, stackErrs: map[int]error{12: errors.New("502 Bad Gateway")}})
		require.ErrorContains(t, err, "failed to get stack #12: 502 Bad Gateway", logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "X")
		r.requireStack("main", "Z")
		require.Equal(t, tips, r.tips("Y"))
		// No stack is found.
		_, logs, err = r.run(nil, run{branch: "main", stacks: stacks, unstacked: []int{11, 12, 13}})
		require.ErrorContains(t, err, "none of the stacks of the pull requests that target \"main\" was found (#11, #12, #13)", logs)
	})

	t.Run("StackOnStack", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("auth", "main")
		r.migration("auth", "20260102000000", "auth1")
		r.branch("api", "auth")
		r.migration("api", "20260103000000", "api1")
		r.branch("ui", "auth")
		r.migration("ui", "20260104000000", "ui1")
		r.push("auth", "api", "ui")
		stacks := []*atlasaction.Stack{
			r.stackAt(5, 1, "main", "auth", "api"),
			r.stackAt(9, 11, "auth", "ui"), // Based on a branch of the stack above.
		}
		// A migration pushed to auth restacks both stacks.
		before := r.tip("auth")
		r.migration("auth", "20260102000100", "auth2")
		r.push("auth")
		act, logs, err := r.run(nil, run{branch: "auth", before: before, stacks: stacks})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "auth", "api")
		r.requireStack("auth", "ui")
		// A migration on main restacks auth, and then ui onto the new auth.
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err = r.run(nil, run{branch: "main", stacks: stacks})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "auth", "api")
		r.requireStack("auth", "ui")
		require.Equal(t, []string{"init", "m1", "auth1", "auth2", "ui1"}, r.names("ui"))
		require.Equal(t, []string{"ui: add ui1", botMessage}, r.log("auth", "ui"), "the old commits of auth are not replayed")
		// auth gets a migration, squashed into its last commit, and its push restacks the stacks on it.
		before = r.tip("auth")
		r.git(r.dev, "fetch", "--quiet", "origin")
		r.git(r.dev, "checkout", "--quiet", "-B", "auth", "origin/auth")
		r.migration("auth", time.Now().UTC().Add(2*time.Second).Format("20060102150405"), "auth3")
		r.git(r.dev, "reset", "--quiet", "--soft", "HEAD~2")
		r.git(r.dev, "commit", "--quiet", "--message", "auth: squashed")
		r.forcePush("auth")
		// Without the force-pushes of auth, its old commits cannot be told from the ones of ui.
		tips := r.tips("api", "ui")
		_, logs, err = r.run(nil, run{branch: "auth", stacks: stacks, unreadable: []int{1}})
		require.ErrorContains(t, err, "stack #9: #11 (ui): cannot find the commit of auth it was built on. Nothing was pushed", logs)
		require.ErrorContains(t, err, "stack #5: #2 (api): cannot find the commit of #1 (auth) it was built on. Nothing was pushed")
		require.Equal(t, tips, r.tips("api", "ui"))
		_, logs, err = r.run(nil, run{branch: "auth", before: before, stacks: stacks})
		require.NoError(t, err, logs)
		r.requireStack("main", "auth", "api")
		r.requireStack("auth", "ui")
		require.Equal(t, []string{"ui: add ui1", botMessage}, r.log("auth", "ui"), "the old commits of auth are not replayed")
		require.Equal(t, []string{"api: add api1", botMessage}, r.log("auth", "api"))
	})

	t.Run("TwoDirectories", func(t *testing.T) {
		r := newStackRepo(t)
		other := func(branch, version, name string) {
			r.checkout(branch)
			dir := filepath.Join(r.dev, "other")
			require.NoError(t, os.MkdirAll(dir, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, version+"_"+name+".sql"), []byte("CREATE TABLE "+name+" (id int);\n"), 0644))
			r.atlas(r.dev, "migrate", "hash", "--dir", "file://other")
			r.git(r.dev, "add", "--all")
			r.git(r.dev, "commit", "--quiet", "--message", branch+": add "+name)
		}
		other("main", "20260101000000", "o0")
		r.push("main")
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		other("A", "20260102000000", "oa1")
		r.push("A")
		s := r.stack(20, "main", "A")
		r.migration("main", "20260105000000", "m1")
		other("main", "20260105000000", "o1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.ErrorContains(t, err, "#1 (A): conflicts with main in other/atlas.sum. Restacks resolve only migrations/atlas.sum, and not the atlas.sum files of other migration directories", logs)
	})

	t.Run("ConflictNames", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(21, "main", "A")
		for _, b := range []string{"A", "main"} {
			r.checkout(b)
			for _, name := range []string{"release notes.md", "café.txt"} {
				require.NoError(t, os.WriteFile(filepath.Join(r.dev, name), []byte(b+"\n"), 0644))
			}
			r.git(r.dev, "add", "--all")
			r.git(r.dev, "commit", "--quiet", "--message", b+": notes")
		}
		r.migration("main", "20260105000000", "m1")
		r.push("main", "A")
		_, logs, err := r.run(s, run{branch: "main"})
		require.ErrorContains(t, err, "#1 (A): conflicts with main in café.txt, release notes.md. Nothing was pushed", logs)
	})

	t.Run("Errors", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.push("A")
		s := r.stack(1, "main", "A")
		other := &atlasaction.Stack{Number: 2, Open: true, Base: "main", Layers: []atlasaction.StackLayer{{Number: 9, Branch: "A", Base: "main", Open: true}}}
		for _, ci := range []atlasexec.TriggerType{atlasexec.TriggerTypeCircleCIOrb, atlasexec.TriggerTypeAzureDevOps, atlasexec.TriggerTypeGitlab} {
			_, _, err := r.run(s, run{branch: "A", ci: ci})
			require.EqualError(t, err, fmt.Sprintf(`the "stack" input is supported only on GitHub Actions, not on %s`, ci))
		}
		_, _, err := r.run(nil, run{branch: "A", stacks: []*atlasaction.Stack{s, other}})
		require.EqualError(t, err, `branch "A" belongs to more than one stack (#1, #2), restack them manually`)

		_, _, err = r.run(s, run{branch: "A", inputs: map[string]string{"base-sha": "0123456789abcdef"}})
		require.EqualError(t, err, `the "stack" input cannot be used with "base-sha" or "force-rebase"`)
		_, _, err = r.run(s, run{branch: "A", inputs: map[string]string{"force-rebase": "true"}})
		require.EqualError(t, err, `the "stack" input cannot be used with "base-sha" or "force-rebase"`)
		// A blank input is not set, as without the "stack" input.
		_, logs, err := r.run(s, run{branch: "A", inputs: map[string]string{"base-sha": " \n"}})
		require.NoError(t, err, logs)
		_, _, err = r.run(s, run{branch: "", scm: func(*stackSCM) atlasaction.SCMClient { return nil }})
		require.EqualError(t, err, `the "stack" input is supported only for repositories hosted on GitHub`)
		_, _, err = r.run(s, run{branch: "A", scm: func(*stackSCM) atlasaction.SCMClient { return &mockSCM{} }})
		require.EqualError(t, err, `the "stack" input is supported only for repositories hosted on GitHub`)
		_, _, err = r.run(s, run{branch: ""})
		require.EqualError(t, err, `failed to determine the branch the action runs on`)

		// A migration directory reached through a symbolic link is not supported.
		require.NoError(t, os.Symlink("migrations", filepath.Join(r.ci, "linked")))
		_, _, err = r.run(s, run{branch: "A", inputs: map[string]string{"dir": "file://linked"}})
		require.ErrorContains(t, err, `restacking does not support a migration directory that is a symbolic link, or under one: "linked"`)
		require.NoError(t, os.Remove(filepath.Join(r.ci, "linked")))

		// A shallow clone cannot be restacked.
		shallow := filepath.Join(t.TempDir(), "shallow")
		r.git("", "clone", "--quiet", "--depth=1", "file://"+r.origin, shallow)
		r.ci = shallow
		_, _, err = r.run(s, run{branch: "A"})
		require.ErrorContains(t, err, "restacking requires the full git history, set `fetch-depth: 0` in actions/checkout")
	})
}

func TestGitHubClientStacks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/stacks/4":
			_, _ = w.Write([]byte(`{"number":4,"open":true,"base":{"ref":"main"},"pull_requests":[` +
				`{"number":1,"state":"closed","merged_at":"2026-11-23T20:10:58Z","head":{"ref":"a","sha":"a1"},"base":{"ref":"main","sha":"m1"}},` +
				`{"number":2,"state":"open","merged_at":null,"head":{"ref":"b","sha":"b1"},"base":{"ref":"main","sha":"m2"}}]}`))
		case "/repos/owner/repo/stacks/6":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c, err := atlasaction.NewGitHubClient("owner/repo", srv.URL, "token")
	require.NoError(t, err)
	s, err := c.Stack(context.Background(), 4)
	require.NoError(t, err)
	require.Equal(t, &atlasaction.Stack{Number: 4, Open: true, Base: "main", Layers: []atlasaction.StackLayer{
		{Number: 1, Branch: "a", Commit: "a1", Base: "main", BaseCommit: "m1", Merged: true},
		{Number: 2, Branch: "b", Commit: "b1", Base: "main", BaseCommit: "m2", Open: true},
	}}, s)
	// A stack that is not found is nil.
	s, err = c.Stack(context.Background(), 5)
	require.NoError(t, err)
	require.Nil(t, s)
	_, err = c.Stack(context.Background(), 6)
	require.ErrorContains(t, err, "unexpected status code 502")
}

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

type (
	// stackRepo is a git remote with a clone that developers (and GitHub) push
	// from, and a clone that the action runs in, as checked out by actions/checkout.
	stackRepo struct {
		t       *testing.T
		dir     string // Migration directory, relative to the repository root.
		origin  string
		dev, ci string
		ciLink  string // Symbolic link to the clone of the action, to run it from.
		// Number of entries in the reflog of each branch when its pull request
		// was opened, as GitHub records the force-pushes only from then on.
		opened map[string]int
	}
	// stackSCM is a GitHub client that serves stacks of pull requests. It reads the
	// force-pushes of their branches from the reflog of the remote.
	stackSCM struct {
		mockSCM
		origin      string
		opened      map[string]int
		prs         []*atlasaction.PullRequest
		stacks      []*atlasaction.Stack
		historyErr  error
		activityErr error         // Error of reading the activity of any branch.
		partial     bool          // Whether the activity of the branches does not go back to their creation.
		noHistory   []int         // Pull requests whose force-pushes and branch activity cannot be read.
		reads       int           // Number of the reads of the activity of branches and the force-pushes of pull requests.
		stackErrs   map[int]error // Errors of the stack lookups.
		unstacked   []int         // Stacks that are not found.
	}
	// otherCI is an action that runs on a CI platform other than GitHub Actions.
	otherCI struct {
		*mockAction
		ci atlasexec.TriggerType
	}
	// run configures a run of the action, on a push to the branch.
	run struct {
		branch    string
		before    string // Head of the branch before the push.
		commit    string // Head of the branch after the push, the head of the branch by default.
		stacks    []*atlasaction.Stack
		prs       []*atlasaction.PullRequest
		noHistory bool // Whether the history of the branches is unknown: their activity, and the force-pushes of their pull requests.
		// activity is "none" if the activity of the branches cannot be read, and "partial"
		// if it does not go back to their creation, like after a retention period.
		activity   string
		noTimeline bool // Whether the force-pushes of the pull requests cannot be read.
		// Pull requests whose force-pushes cannot be read, while the others can.
		unreadable []int
		stackErrs  map[int]error         // Errors of the stack lookups.
		unstacked  []int                 // Stacks that are unstacked after their pull requests were listed.
		ci         atlasexec.TriggerType // The CI platform the action runs on, GitHub Actions by default.
		inputs     map[string]string
		exec       func(context.Context, string, ...string) *exec.Cmd
		wd         string // Working directory, relative to the clone.
		scm        func(*stackSCM) atlasaction.SCMClient
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
		opened: make(map[string]int),
	}
	if len(dir) > 0 && dir[0] != "" {
		r.dir = dir[0]
	}
	r.git("", "init", "--quiet", "--bare", "--initial-branch=main", r.origin)
	// Keep the history of the branches, from which force-pushes are reported, as GitHub does.
	r.git(r.origin, "config", "core.logAllRefUpdates", "always")
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
	r.migrationSQL(branch, version, name, fmt.Sprintf("CREATE TABLE %s (id int);\n", name))
}

// migrationSQL adds a migration file with the given contents to the branch, and commits it with the updated atlas.sum.
func (r *stackRepo) migrationSQL(branch, version, name, sql string) {
	r.checkout(branch)
	dir := filepath.Join(r.dev, r.dir)
	require.NoError(r.t, os.MkdirAll(dir, 0755))
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, version+"_"+name+".sql"), []byte(sql), 0644))
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
	r.git(r.dev, append([]string{"push", "--quiet", "origin"}, refspecs(branches)...)...)
}

// forcePush force-pushes the branches of the developer clone.
func (r *stackRepo) forcePush(branches ...string) {
	r.git(r.dev, append([]string{"push", "--quiet", "--force", "origin"}, refspecs(branches)...)...)
}

// refspecs returns the refspecs that push the branches, even if tags have their names.
func refspecs(branches []string) []string {
	specs := make([]string, len(branches))
	for i, b := range branches {
		specs[i] = fmt.Sprintf("refs/heads/%s:refs/heads/%s", b, b)
	}
	return specs
}

// land lands the bottom layer i of the stack on its base, with a squash merge or the
// given merge method, and retargets the layer above it, as GitHub does. It keeps the
// head of the merged layer as the head ref of its pull request.
func (r *stackRepo) land(s *atlasaction.Stack, i int, method ...string) {
	l := &s.Layers[i]
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.checkout(s.Base)
	r.git(r.dev, "reset", "--quiet", "--hard", "origin/"+s.Base)
	if len(method) > 0 && method[0] == "merge" {
		r.git(r.dev, "merge", "--quiet", "--no-ff", "--message", fmt.Sprintf("Merge %s (#%d)", l.Branch, l.Number), "origin/"+l.Branch)
	} else {
		r.git(r.dev, "merge", "--quiet", "--squash", "origin/"+l.Branch)
		r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("Squash %s (#%d)", l.Branch, l.Number))
	}
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
	return strings.Split(r.gitOut(r.origin, "log", "--reverse", "--no-merges", "--format=%s", base+".."+branch), "\n")
}

// sum returns the atlas.sum of the remote branch.
func (r *stackRepo) sum(branch string) migrate.HashFile {
	r.t.Helper()
	var hf migrate.HashFile
	require.NoError(r.t, hf.UnmarshalText([]byte(r.gitOut(r.origin, "show", branch+":"+filepath.ToSlash(filepath.Join(r.dir, "atlas.sum"))))))
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
	return r.stackAt(n, 1, base, branches...)
}

// stackAt returns a stack of the given branches, whose pull requests are numbered from first.
// Their pull requests are opened now.
func (r *stackRepo) stackAt(n, first int, base string, branches ...string) *atlasaction.Stack {
	s := &atlasaction.Stack{Number: n, Open: true, Base: base}
	for i, b := range branches {
		r.opened[b] = len(strings.Fields(r.gitOut(r.origin, "reflog", "show", "--format=%H", "refs/heads/"+b)))
		l := atlasaction.StackLayer{Number: first + i, Branch: b, Commit: r.tip(b), Base: base, Open: true}
		if i > 0 {
			l.Base = branches[i-1]
		}
		if c, err := exec.Command("git", "-C", r.origin, "rev-parse", "--verify", "--quiet", "refs/heads/"+l.Base).Output(); err == nil {
			l.BaseCommit = strings.TrimSpace(string(c))
		}
		// GitHub keeps the head of the pull request as refs/pull/<number>/head.
		r.git(r.origin, "update-ref", fmt.Sprintf("refs/pull/%d/head", l.Number), l.Commit)
		s.Layers = append(s.Layers, l)
	}
	return s
}

// run runs the action on a push to the branch, in the clone of the action.
func (r *stackRepo) run(s *atlasaction.Stack, o run) (*mockAction, string, error) {
	r.t.Helper()
	scm := &stackSCM{
		origin:    r.origin,
		opened:    r.opened,
		prs:       o.prs,
		stacks:    o.stacks,
		noHistory: o.unreadable,
		stackErrs: o.stackErrs,
		unstacked: o.unstacked,
	}
	if s != nil {
		scm.stacks = append(scm.stacks, s)
	}
	if o.noHistory || o.noTimeline {
		scm.historyErr = errors.New("history is not available")
	}
	if o.noHistory || o.activity == "none" {
		scm.activityErr = errors.New("activity is not available")
	}
	scm.partial = o.activity == "partial"
	branch := strings.TrimPrefix(o.branch, "refs/heads/")
	// Check out the branch, as actions/checkout does with fetch-depth: 0.
	r.git(r.ci, "fetch", "--quiet", "--prune", "--force", "--tags", "origin", "+refs/heads/*:refs/remotes/origin/*")
	if branch != "" {
		r.git(r.ci, "checkout", "--quiet", "--force", "-B", branch, "refs/remotes/origin/"+branch)
	}
	ci := r.ci
	if r.ciLink != "" {
		ci = r.ciLink
	}
	r.t.Chdir(filepath.Join(ci, o.wd))
	in := map[string]string{"stack": "true", "dir": "file://" + r.dir}
	for k, v := range o.inputs {
		in[k] = v
	}
	commit := o.commit
	if commit == "" && branch != "" {
		commit = r.tip(branch)
	}
	act := &mockAction{
		inputs: in,
		trigger: &atlasaction.TriggerContext{
			SCMType:       atlasexec.SCMTypeGithub,
			Branch:        o.branch,
			DefaultBranch: "main",
			Commit:        commit,
			Before:        o.before,
			SCMClient:     func() (atlasaction.SCMClient, error) { return scm, nil },
		},
	}
	if o.scm != nil {
		c := o.scm(scm)
		act.trigger.SCMClient = func() (atlasaction.SCMClient, error) { return c, nil }
		if c == nil {
			act.trigger.SCMClient = nil
		}
	}
	var logs bytes.Buffer
	act.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cli, err := atlasexec.NewClient("", "atlas")
	require.NoError(r.t, err)
	run := o.exec
	if run == nil {
		run = exec.CommandContext
	}
	var action atlasaction.Action = act
	if o.ci != "" {
		action = &otherCI{mockAction: act, ci: o.ci}
	}
	acts, err := atlasaction.New(
		atlasaction.WithAction(action),
		atlasaction.WithAtlas(cli),
		atlasaction.WithCmdExecutor(run),
	)
	require.NoError(r.t, err)
	err = acts.MigrateAutoRebase(context.Background())
	return act, logs.String(), err
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
	if err := s.stackErrs[n]; err != nil {
		return nil, err
	}
	if slices.Contains(s.unstacked, n) {
		return nil, nil
	}
	for _, st := range s.stacks {
		if st.Number == n {
			return st, nil
		}
	}
	return nil, nil
}

// GetType implements atlasaction.Action.
func (o *otherCI) GetType() atlasexec.TriggerType {
	return o.ci
}

// BranchHeads implements atlasaction.StackClient. Like the activity of a GitHub repository,
// it reports every head the branch had since it was created, from the reflog of the remote.
func (s *stackSCM) BranchHeads(_ context.Context, branch string) ([]string, bool, error) {
	s.reads++
	if s.activityErr != nil {
		return nil, false, s.activityErr
	}
	for _, n := range s.noHistory {
		if s.branchOf(n) == branch {
			return nil, false, fmt.Errorf("activity of %q is not available", branch)
		}
	}
	out, err := exec.Command("git", "-C", s.origin, "reflog", "show", "--format=%H", "refs/heads/"+branch).Output()
	if err != nil {
		return nil, false, err
	}
	heads := slices.Compact(strings.Fields(string(out)))
	if s.partial {
		// Only the last change is still recorded.
		heads = heads[:min(2, len(heads))]
	}
	return heads, !s.partial, nil
}

// branchOf returns the head branch of the pull request.
func (s *stackSCM) branchOf(n int) string {
	var branch string
	for _, pr := range s.prs {
		if pr.Number == n {
			branch = pr.Ref
		}
	}
	for _, st := range s.stacks {
		for _, l := range st.Layers {
			if l.Number == n {
				branch = l.Branch
			}
		}
	}
	return branch
}

// ForcePushes implements atlasaction.StackClient. Like GitHub, it reports only
// the force-pushes since the pull request was opened.
func (s *stackSCM) ForcePushes(_ context.Context, nums []int) (map[int][]string, error) {
	s.reads++
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	pushes := make(map[int][]string)
	for _, n := range nums {
		if slices.Contains(s.noHistory, n) {
			continue
		}
		branch := s.branchOf(n)
		if branch == "" {
			continue
		}
		// The reflog lists the heads of the branch, newest first.
		out, err := exec.Command("git", "-C", s.origin, "reflog", "show", "--format=%H", "refs/heads/"+branch).Output()
		if err != nil {
			return nil, err
		}
		heads := strings.Fields(string(out))
		slices.Reverse(heads)
		pushes[n] = []string{}
		for i := max(1, s.opened[branch]); i < len(heads); i++ {
			if err := exec.Command("git", "-C", s.origin, "merge-base", "--is-ancestor", heads[i-1], heads[i]).Run(); err != nil {
				pushes[n] = append(pushes[n], heads[i-1], heads[i])
			}
		}
	}
	return pushes, nil
}

var _ atlasaction.StackClient = (*stackSCM)(nil)
