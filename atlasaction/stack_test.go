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
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-action/internal/github"
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
		r.land(s)
		r.rebase("B", "main", oldA)
		r.rebase("C", "B", oldB)
		act, logs, err = r.run(s, run{branch: "C"})
		require.NoError(t, err, logs)
		require.Equal(t, "false", act.output["rebased"])
		r.requireStack("main", "B", "C")
	})

	t.Run("LowerLayerPushed", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			branch    string // The branch the action runs on.
			history   bool   // Whether the history of A is known.
			before    bool   // Whether the push event has the head A had before the push.
			wantError string
		}{
			{name: "PushEvent", branch: "A", history: true},
			{name: "History", branch: "C", history: true},
			// B is built on the head A had before the push, which the push event has.
			{name: "NoHistoryBefore", branch: "A", before: true},
			// Without the history, the heads A is known to have had cannot tell whether
			// it dropped commits that B has, and B is not built on the head A has now.
			{name: "NoHistory", branch: "A", wantError: "#2 (B): the activity of branch \"A\" cannot be read. It is not built on the latest head of #1 (A), so its commits cannot be told from the ones #1 (A) had. Nothing was pushed"},
			{name: "Unknown", branch: "C", wantError: "#2 (B): the activity of branch \"A\" cannot be read. It is not built on the latest head of #1 (A), so its commits cannot be told from the ones #1 (A) had. Nothing was pushed"},
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
				before := r.tip("A")
				// The developer amends A, and adds a migration that sorts after the ones above it.
				r.amend("A", "A: add a1 (amended)")
				r.migration("A", time.Now().UTC().Format("20060102150405"), "a2")
				r.forcePush("A")
				o := run{branch: tt.branch, noHistory: !tt.history}
				if tt.before {
					o.before = before
				}
				tips := r.tips("A", "B", "C")
				act, logs, err := r.run(s, o)
				if tt.wantError != "" {
					require.ErrorContains(t, err, tt.wantError)
					require.Contains(t, logs, `failed to read the activity of branch \"A\"`)
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
		// A drops it, gets another migration, and is force-pushed.
		r.checkout("A")
		r.git(r.dev, "reset", "--quiet", "--hard", a1)
		r.migration("A", time.Now().UTC().Format("20060102150405"), "a2")
		r.forcePush("A")
		// The run of the first push to A runs only now. B is built on the dropped migration, which A
		// may have dropped or moved to B, so B is left as it is, and the dropped migration is named.
		tips := r.tips("A", "B")
		_, logs, err := r.run(s, run{branch: "A", before: a1, commit: a2})
		require.Error(t, err, logs)
		require.Regexp(t, `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add bad\)\. Nothing was pushed`, err.Error())
		require.Equal(t, tips, r.tips("A", "B"), "nothing is pushed")
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
		r.land(s)

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
				r.land(s)

				tips := r.tips("B")
				act, logs, err := r.run(s, run{branch: "main", noHistory: !history})
				if !history {
					require.ErrorContains(t, err, "#2 (B): the activity of branch \"A\" cannot be read. It is not built on the latest head of the merged #1 (A), so its commits cannot be told from the ones the merged #1 (A) had. Nothing was pushed", logs)
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
		r.land(s, "merge")
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

		// A version far ahead of the clock is not waited for.
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
		// develop has no pull request, and the push that triggers the run rewrites it.
		squash := func(r *stackRepo, message string) {
			r.git(r.dev, "reset", "--quiet", "--soft", "main")
			r.git(r.dev, "commit", "--quiet", "--message", message)
		}
		for _, tt := range []struct {
			name      string
			old, new  func(*stackRepo) // The commits of develop before and after the push.
			wantError string
		}{
			{
				// The commit of develop no longer adds y.txt, which A may have, or not.
				name: "Dropped",
				old: func(r *stackRepo) {
					r.file("develop", "x.txt", "x\n")
					r.file("develop", "y.txt", "y\n")
					squash(r, "develop: add x and y")
				},
				new:       func(r *stackRepo) { r.file("develop", "x.txt", "x\n") },
				wantError: `#1 \(A\): it has commits that develop no longer has, which a restack would drop: [0-9a-f]{7} \(develop: add x and y\)\. Nothing was pushed`,
			},
			{
				// The commits of develop are squashed, and keep their changes.
				name: "Squashed",
				old: func(r *stackRepo) {
					r.file("develop", "x.txt", "x\n")
					r.file("develop", "y.txt", "y\n")
				},
				new: func(r *stackRepo) {
					r.file("develop", "x.txt", "x\n")
					r.file("develop", "y.txt", "y\n")
					squash(r, "develop: add x and y")
				},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("develop", "main")
				r.checkout("develop")
				tt.old(r)
				r.branch("A", "develop")
				r.migration("A", "20260102000000", "a1")
				r.push("develop", "A")
				s := r.stack(15, "develop", "A")
				before := r.tip("develop")
				r.checkout("develop")
				r.git(r.dev, "reset", "--quiet", "--hard", "main")
				tt.new(r)
				r.migration("develop", time.Now().UTC().Format("20060102150405"), "d1")
				r.forcePush("develop")

				tips := r.tips("A")
				act, logs, err := r.run(s, run{branch: "develop", before: before})
				if tt.wantError != "" {
					require.Error(t, err, logs)
					require.Regexp(t, tt.wantError, err.Error())
					require.Equal(t, tips, r.tips("A"), "nothing is pushed")
					return
				}
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"])
				r.requireStack("develop", "A")
				require.Equal(t, []string{"A: add a1", botMessage}, r.log("develop", "A"), "the old commits of develop are not replayed")
			})
		}
	})

	t.Run("SplitBranch", func(t *testing.T) {
		// A developer splits A into a stack: B is created from A, and A drops the commit that moved to B.
		for _, tt := range []struct {
			name    string
			moved   func(*stackRepo) // Adds the commit that moves from A to B.
			replace func(*stackRepo) // Adds the commit A gets instead, if any.
			subject string           // Subject of the moved commit.
		}{
			{
				name:    "Dropped",
				moved:   func(r *stackRepo) { r.file("A", "feature.txt", "feature\n") },
				subject: "A: feature.txt",
			},
			{
				// A gets an unrelated commit with the same subject.
				name: "SameSubject",
				moved: func(r *stackRepo) {
					r.file("A", "feature.txt", "feature\n")
					r.amend("A", "fix")
				},
				replace: func(r *stackRepo) {
					r.file("A", "other.txt", "other\n")
					r.amend("A", "fix")
				},
				subject: "fix",
			},
			{
				name: "SameSubjectMigration",
				moved: func(r *stackRepo) {
					r.migration("A", "20260102000100", "x")
					r.amend("A", "add migration")
				},
				replace: func(r *stackRepo) {
					r.migration("A", "20260102000200", "y")
					r.amend("A", "add migration")
				},
				subject: "add migration",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				tt.moved(r)
				r.push("A")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.checkout("A")
				r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
				if tt.replace != nil {
					tt.replace(r)
				}
				r.forcePush("A")
				r.push("B")
				s := r.stack(17, "main", "A", "B")
				r.migration("main", "20260105000000", "m1")
				r.push("main")

				tips := r.tips("A", "B")
				_, logs, err := r.run(s, run{branch: "main"})
				require.Error(t, err, logs)
				require.Regexp(t, `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(`+regexp.QuoteMeta(tt.subject)+`\)\. Nothing was pushed`, err.Error())
				require.Equal(t, tips, r.tips("A", "B"), "the commit moved to B is not dropped")
			})
		}
	})

	t.Run("AmendedBelow", func(t *testing.T) {
		// A commit of A that B was built on is amended, and changes a file other than migrations.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.checkout("A")
		require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte("v1\n"), 0644))
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(26, "main", "A", "B")
		r.checkout("A")
		require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte("v2\n"), 0644))
		r.git(r.dev, "commit", "--quiet", "--all", "--amend", "--no-edit")
		r.forcePush("A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
		require.Equal(t, "v2", r.gitOut(r.origin, "show", "B:app.txt"))
	})

	t.Run("ForcePushedAway", func(t *testing.T) {
		// B is built on a1, the first of the two commits A was pushed with. A then rewrites a1, and drops a2.
		// The clone of the run does not have a2, the head that tells where B starts, so it fetches it.
		for _, tt := range []struct {
			name    string
			debug   string // Contents of debug.txt in the new a1, if any.
			pruned  bool   // Whether the remote no longer has a head that A had.
			refused string // Regular expression of the error, if the restack is refused.
		}{
			{name: "Amended", debug: "v2\n"},
			// The new a1 drops debug.txt, which B may have, or not.
			{name: "Dropped", refused: `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add a1\)\. Nothing was pushed`},
			{name: "Pruned", debug: "v2\n", pruned: true, refused: `(?s)#2 \(B\): cannot fetch the heads #1 \(A\) had: .*not our ref ` + prunedHead + `\. Nothing was pushed`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.checkout("A")
				require.NoError(t, os.WriteFile(filepath.Join(r.dev, "debug.txt"), []byte("v1\n"), 0644))
				r.migration("A", "20260102000000", "a1")
				a1 := r.gitOut(r.dev, "rev-parse", "HEAD")
				r.migration("A", "20260102000100", "a2")
				r.push("A")
				r.branch("B", a1)
				r.migration("B", "20260103000000", "b1")
				r.push("B")
				s := r.stack(27, "main", "A", "B")
				r.checkout("A")
				r.git(r.dev, "reset", "--quiet", "--hard", "main")
				if tt.debug != "" {
					require.NoError(t, os.WriteFile(filepath.Join(r.dev, "debug.txt"), []byte(tt.debug), 0644))
				}
				r.migration("A", "20260102000000", "a1")
				r.forcePush("A")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("A", "B")
				o := run{branch: "main"}
				if tt.pruned {
					o.pruned = []string{"A"}
				}
				act, logs, err := r.run(s, o)
				if tt.refused != "" {
					require.Error(t, err, logs)
					require.Regexp(t, tt.refused, err.Error())
					require.Equal(t, "false", act.output["rebased"])
					require.Equal(t, tips, r.tips("A", "B"), "nothing is pushed")
					return
				}
				require.NoError(t, err, logs)
				r.requireStack("main", "A", "B")
				require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"), "the old a1 is not replayed")
				require.Equal(t, "v2", r.gitOut(r.origin, "show", "B:debug.txt"))
			})
		}
	})

	t.Run("PrunedHeadAbove", func(t *testing.T) {
		// The remote no longer has a head that A had, and B conflicts with A, which does not conflict with main.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(28, "main", "A", "B")
		r.migration("A", "20260102000100", "a2")
		r.push("A")
		tips := r.tips("A", "B")
		act, logs, err := r.run(s, run{branch: "A", pruned: []string{"A"}})
		require.Error(t, err, logs)
		require.Regexp(t, `(?s)#2 \(B\): cannot fetch the heads #1 \(A\) had: .*not our ref `+prunedHead+`\. Nothing was pushed`, err.Error())
		require.Equal(t, "false", act.output["rebased"])
		require.Equal(t, tips, r.tips("A", "B"))
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
				run: run{branch: "main", inputs: map[string]string{"dir": "file://."}, keep: true},
				check: func(t *testing.T, r *stackRepo, _ *mockAction) {
					_, err := os.Stat(filepath.Join(r.ci, "atlas-action"))
					require.NoError(t, err, "files that git does not track are kept")
					require.NotContains(t, r.gitOut(r.origin, "ls-tree", "--name-only", "A"), "atlas-action")
				},
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
				// Atlas reads the directive on the first line, after any prefix.
				name: "SumIgnoreHashComment",
				setup: func(r *stackRepo) {
					r.checkout("A")
					r.migrationSQL("A", "20260102000001", "seed", "#atlas:sum ignore\nINSERT INTO init VALUES (1);\n")
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
			name    string
			rewrite func(*stackRepo)
			want    []string // Migrations of B with the activity of A.
			refused string   // Regular expression of the error with the activity of A, if the restack is refused.
		}{
			{
				// a1 is amended in place, as in `git commit --amend`.
				name: "Amended",
				rewrite: func(r *stackRepo) {
					r.checkout("A")
					r.git(r.dev, "reset", "--quiet", "--hard", "main")
					r.migrationSQL("A", "20260102000000", "a1", "CREATE TABLE a1 (id bigint);\n")
				},
				want: []string{"init", "m1", "a1", "b1"},
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
				want: []string{"init", "m0", "m1", "a1", "b1"},
			},
			{
				// A drops a migration that B was built on. Only the activity of A tells, and
				// as the migration may have been moved to B, it is not dropped from B.
				name: "Rewound",
				rewrite: func(r *stackRepo) {
					r.checkout("A")
					r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
				},
				refused: `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add bad\)\. Nothing was pushed`,
			},
		} {
			for _, activity := range []string{"", "partial"} {
				if activity != "" && tt.refused != "" {
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
					if activity == "partial" || tt.refused != "" {
						want := tt.refused
						if activity == "partial" {
							// Without the heads A had before it was rewritten, its commits cannot be told from the ones of B.
							want = `#2 \(B\): the activity of branch "A" does not go back to its creation, and has force-pushes\. It is not built on the latest head of #1 \(A\), so .*\. Nothing was pushed`
						}
						require.Error(t, err, logs)
						require.Regexp(t, want, err.Error())
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

	t.Run("ActivityError", func(t *testing.T) {
		// The activity is not available, like on GitHub Enterprise Server, or reading it fails, like on a
		// rate limit. The history of A is unknown, but B is built on the head A has, which is enough.
		for _, activity := range []string{"none", "error"} {
			t.Run(activity, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(23, "main", "A", "B")
				// The run of the push to main knows the head main had before, which A is built on.
				before := r.tip("main")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				act, logs, err := r.run(s, run{branch: "main", before: before, activity: activity})
				require.NoError(t, err, logs)
				require.Contains(t, logs, `failed to read the activity of branch \"A\"`)
				require.Contains(t, logs, `#2 (B): the activity of branch \"A\" cannot be read. It is built on the latest head of #1 (A), so its own commits start there`)
				require.Equal(t, "true", act.output["rebased"])
				r.requireStack("main", "A", "B")
				require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
			})
		}
	})

	t.Run("SharedTrunkHistory", func(t *testing.T) {
		// Two stacks target develop, which has its own pull request.
		r := newStackRepo(t)
		r.branch("develop", "main")
		r.file("develop", "develop.txt", "develop\n")
		r.branch("A", "develop")
		r.migration("A", "20260102000000", "a1")
		r.branch("C", "develop")
		r.migration("C", "20260103000000", "c1")
		r.push("develop", "A", "C")
		s1, s2 := r.stack(24, "develop", "A"), r.stackAt(25, 3, "develop", "C")
		r.migration("develop", "20260105000000", "d1")
		r.push("develop")
		var scm *stackSCM
		_, logs, err := r.run(s1, run{
			branch: "develop",
			stacks: []*atlasaction.Stack{s2},
			prs:    []*atlasaction.PullRequest{{Number: 99, Ref: "develop", Base: "main"}},
			scm:    func(s *stackSCM) atlasaction.SCMClient { scm = s; return s },
		})
		require.NoError(t, err, logs)
		r.requireStack("develop", "A")
		r.requireStack("develop", "C")
		require.Equal(t, 1, scm.reads, "the history of develop is read once")
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
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("A", "B")
				act, logs, err := r.run(s, run{branch: "main", noHistory: !history})
				// With the history of A, the commit A dropped is named, as it may have been moved to B.
				if !history {
					// B is built on the head A has now, and keeps the commit A dropped, as GitHub shows it in B.
					require.NoError(t, err, logs)
					require.Contains(t, logs, "the commits #1 (A) dropped before, if any, stay in it")
					require.Equal(t, "true", act.output["rebased"])
					r.requireStack("main", "A", "B")
					require.Equal(t, []string{"init", "m1", "a1", "bad", "b1"}, r.names("B"))
					return
				}
				want := `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add bad\)\. Nothing was pushed`
				require.Error(t, err, logs)
				require.Regexp(t, want, err.Error())
				require.Equal(t, "false", act.output["rebased"])
				require.Equal(t, tips, r.tips("A", "B"))
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
		r.land(s)
		// C gets a migration that D is not restacked on.
		r.migration("C", "20260104000050", "c2")
		r.push("C")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// The history of #3 cannot be read, which stops only the restack that needs it.
		tips := r.tips("B", "C", "D")
		_, logs, err := r.run(s, run{branch: "main", unreadable: []int{3}})
		require.ErrorContains(t, err, `stack #7: #4 (D): the activity of branch "C" cannot be read. It is not built on the latest head of #3 (C), so its commits cannot be told from the ones #3 (C) had. Nothing was pushed`, logs)
		require.NotContains(t, err.Error(), "#2 (B)")
		require.Contains(t, logs, `failed to read the activity of branch \"C\"`)
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
			octopus   bool   // Whether the merge commit also merges another branch.
			wantError string // Regular expression.
		}{
			{name: "Clean"},
			{name: "OwnChanges", own: true, wantError: `#1 \(A\): merge commit [0-9a-f]{7} has changes of its own in fix\.txt, which a restack drops\. Nothing was pushed`},
			// Git cannot tell the changes of its own of an octopus merge, even if it has none.
			{name: "Octopus", octopus: true, wantError: `#1 \(A\): merge commit [0-9a-f]{7} merges 3 commits, and a restack drops it without checking it for changes of its own\. Nothing was pushed`},
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
				merge := []string{"merge", "--quiet", "--no-ff", "--no-commit", "main"}
				if tt.octopus {
					r.branch("X", "main~1")
					r.file("X", "x.txt", "x\n")
					merge = append(merge, "X")
				}
				// The developer merges the trunk into A.
				r.checkout("A")
				r.git(r.dev, merge...)
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

	t.Run("LongLivedTrunk", func(t *testing.T) {
		// develop has a long-lived pull request, and its activity does not go back to its creation.
		for _, tt := range []struct {
			name      string
			pushes    int // Pushes to develop after A was built on it.
			wantError string
		}{
			// The activity has the head A was built on, and develop only moved forward since.
			{name: "Recorded", pushes: 1},
			{name: "NotRecorded", pushes: 2, wantError: `#1 (A): the activity of branch "develop" does not go back to the head the layer was built on. It is not built on the latest head of develop, so its commits cannot be told from the ones develop had. Nothing was pushed`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("develop", "main")
				r.file("develop", "develop.txt", "develop\n")
				r.branch("A", "develop")
				r.migration("A", "20260102000000", "a1")
				r.push("develop", "A")
				s := r.stack(14, "develop", "A")
				for i := range tt.pushes {
					r.migration("develop", fmt.Sprintf("2026010500000%d", i), fmt.Sprintf("d%d", i))
					r.push("develop")
				}
				tips := r.tips("A")
				act, logs, err := r.run(s, run{branch: "develop", activity: "partial", prs: []*atlasaction.PullRequest{{Number: 99, Ref: "develop", Base: "main"}}})
				if tt.wantError != "" {
					require.ErrorContains(t, err, tt.wantError, logs)
					require.Equal(t, tips, r.tips("A"), "nothing is pushed")
					return
				}
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"])
				r.requireStack("develop", "A")
				require.Equal(t, []string{"A: add a1", botMessage}, r.log("develop", "A"))
			})
		}
	})

	t.Run("TrunkMergedInLayer", func(t *testing.T) {
		// A layer merged the trunk, and the trunk reverted some of it later. The restack replays only the commits
		// of the layer, and does not bring back what the trunk removed.
		t.Run("BottomAfterSquash", func(t *testing.T) {
			r := newStackRepo(t)
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.file("A", "feature.txt", "A\n")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(41, "main", "A", "B")
			// The trunk moves, and B merges it.
			r.file("main", "other.txt", "o\n")
			r.push("main")
			r.checkout("B")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--message", "Merge main into B", "main")
			r.push("B")
			// A lands with a squash merge, then the trunk removes feature.txt and gets a migration.
			r.land(s)
			r.checkout("main")
			r.git(r.dev, "rm", "--quiet", "feature.txt")
			r.git(r.dev, "commit", "--quiet", "--message", "main: revert feature")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			_, logs, err := r.run(s, run{branch: "main"})
			require.NoError(t, err, logs)
			r.requireStack("main", "B")
			require.Empty(t, r.gitOut(r.origin, "ls-tree", "B", "--", "feature.txt"), "feature.txt, which main removed, is not brought back to B")
			require.NotContains(t, r.log("main", "B"), "A: feature.txt", logs)
		})
		t.Run("BottomTrunkChange", func(t *testing.T) {
			r := newStackRepo(t)
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.file("A", "feature.txt", "A\n")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(43, "main", "A", "B")
			r.file("main", "other.txt", "o\n")
			r.push("main")
			r.checkout("B")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--message", "Merge main into B", "main")
			r.push("B")
			r.land(s)
			r.checkout("main")
			r.git(r.dev, "rm", "--quiet", "feature.txt", "other.txt")
			r.git(r.dev, "commit", "--quiet", "--message", "main: revert feature and other")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			_, logs, err := r.run(s, run{branch: "main"})
			require.NoError(t, err, logs)
			for _, f := range []string{"feature.txt", "other.txt"} {
				require.Empty(t, r.gitOut(r.origin, "ls-tree", "B", "--", f), "%s, which main removed, is not brought back to B", f)
			}
		})
		t.Run("Upper", func(t *testing.T) {
			r := newStackRepo(t)
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.branch("C", "B")
			r.migration("C", "20260103000100", "c1")
			r.push("A", "B", "C")
			s := r.stack(44, "main", "A", "B", "C")
			r.file("main", "other.txt", "o\n")
			r.push("main")
			r.checkout("C")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--message", "Merge main into C", "main")
			r.push("C")
			r.checkout("main")
			r.git(r.dev, "rm", "--quiet", "other.txt")
			r.git(r.dev, "commit", "--quiet", "--message", "main: revert other")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			_, logs, err := r.run(s, run{branch: "main"})
			require.NoError(t, err, logs)
			require.Empty(t, r.gitOut(r.origin, "ls-tree", "C", "--", "other.txt"), "other.txt, which main removed, is not brought back to C")
		})
	})

	t.Run("CherryPickedThenEdited", func(t *testing.T) {
		// B cherry-picked a fix of A, and A edited the same lines again. Replaying B from the head of A
		// skips the cherry-pick, which would conflict with A otherwise.
		r := newStackRepo(t)
		r.file("main", "app.txt", "v1\n")
		r.push("main")
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(40, "main", "A", "B")
		// A gets a fix, which B cherry-picks.
		r.file("A", "app.txt", "v2\n")
		fix := r.gitOut(r.dev, "rev-parse", "HEAD")
		r.push("A")
		r.checkout("B")
		r.git(r.dev, "cherry-pick", fix)
		r.push("B")
		// A changes the same line again, and adds a migration that sorts after b1.
		r.checkout("A")
		require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte("v3\n"), 0644))
		r.git(r.dev, "commit", "--quiet", "--all", "--message", "A: v3")
		r.migration("A", "20260104000000", "a2")
		r.push("A")
		_, logs, err := r.run(s, run{branch: "A"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A", "B")
		require.Equal(t, "v3", r.gitOut(r.origin, "show", "B:app.txt"))

	})

	t.Run("BotCommitReplaced", func(t *testing.T) {
		// The developer replaces the commit of the action in A with a rebase of their own, which is not
		// a commit B needs, so it is not reported as dropped.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(42, "main", "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, []string{"A: add a1", botMessage}, r.log("main", "A"))
		time.Sleep(1100 * time.Millisecond)
		// The developer drops the commit of the action from A, and rebases a1 with a commit of their own.
		r.git(r.dev, "fetch", "--quiet", "origin")
		r.git(r.dev, "checkout", "--quiet", "-B", "A", "origin/A")
		r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
		r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
		r.atlas(r.dev, "migrate", "rebase", "--dir", "file://"+r.dir, "20260102000000")
		r.git(r.dev, "add", "--all")
		r.git(r.dev, "commit", "--quiet", "--message", "A: rebase a1")
		r.forcePush("A")
		_, logs, err = r.run(s, run{branch: "A"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))

	})

	t.Run("FailedStackLeavesNoFiles", func(t *testing.T) {
		// The restack of the first stack fails after atlas migrate rebase renamed its files, and the
		// second stack is restacked in the same run, without the files of the first one.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("X", "main")
		r.migration("X", "20260103000000", "x1")
		r.push("A", "X")
		s1 := r.stackAt(50, 1, "main", "A")
		s2 := r.stackAt(51, 2, "main", "X")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		failed := false
		fail := func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name == "git" && len(args) > 0 && args[0] == "add" && !failed {
				failed = true
				return exec.CommandContext(ctx, "false")
			}
			return exec.CommandContext(ctx, name, args...)
		}
		_, logs, err := r.run(nil, run{branch: "main", stacks: []*atlasaction.Stack{s1, s2}, exec: fail})
		require.True(t, failed)
		require.ErrorContains(t, err, "#1 (A)", logs)
		require.Equal(t, []string{"init", "m1", "x1"}, r.names("X"), logs)

	})

	t.Run("CopiedMigration", func(t *testing.T) {
		// B cherry-picked a2 from A, then A moved a2 to a new version. The restack would leave B with two
		// copies of a2, so it refuses.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(52, "main", "A", "B")
		r.migration("A", "20260102000100", "a2")
		a2 := r.gitOut(r.dev, "rev-parse", "HEAD")
		r.push("A")
		r.checkout("B")
		r.git(r.dev, "cherry-pick", "--strategy-option=theirs", a2)
		r.hash("B")
		r.push("B")
		// A moves a2 to a new version, in place.
		r.checkout("A")
		r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
		r.migration("A", "20260104000000", "a2")
		r.forcePush("A")
		tips := r.tips("A", "B")
		_, logs, err := r.run(s, run{branch: "A"})
		require.Error(t, err, logs)
		require.ErrorContains(t, err, "copies of migration files")
		require.Equal(t, tips, r.tips("A", "B"))

	})

	t.Run("DirtyClone", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(53, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// Changes of tracked files.
		dirty := func(ctx context.Context, name string, args ...string) *exec.Cmd {
			if name == "git" && slices.Equal(args, []string{"status", "--porcelain", "--untracked-files=no"}) {
				return exec.CommandContext(ctx, "echo", " M app.txt")
			}
			return exec.CommandContext(ctx, name, args...)
		}
		tips := r.tips("A")
		_, logs, err := r.run(s, run{branch: "main", exec: dirty})
		require.ErrorContains(t, err, "restacking requires a clean working tree, found changes in:", logs)
		require.ErrorContains(t, err, "app.txt")
		require.Equal(t, tips, r.tips("A"))
		// A migration file that git does not track, which atlas would hash with the directory.
		require.NoError(t, os.WriteFile(filepath.Join(r.ci, r.dir, "20260106000000_local.sql"), []byte("SELECT 1;\n"), 0644))
		_, logs, err = r.run(s, run{branch: "main", keep: true})
		require.ErrorContains(t, err, "restacking requires a clean migration directory", logs)
		require.Equal(t, tips, r.tips("A"))
	})

	t.Run("MergedBelowNoActivity", func(t *testing.T) {
		// A lands, GitHub rebases B onto the trunk, and the trunk gets a migration. Without the activity of A,
		// the run of the push to the trunk knows the head it had before, which B is built on. A run of another
		// push does not, and cannot tell whether B still has commits of A.
		for _, tt := range []struct {
			name, branch, wantError string
		}{
			{name: "PushToTrunk", branch: "main"},
			{name: "PushToLayer", branch: "B", wantError: `#2 (B): the activity of branch "A" cannot be read. It is not built on the latest head of the merged #1 (A)`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(54, "main", "A", "B")
				oldA := r.tip("A")
				r.land(s)
				r.rebase("B", "main", oldA)
				before := r.tip("main")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("B")
				o := run{branch: tt.branch, noHistory: true}
				if tt.branch == "main" {
					o.before = before
				}
				_, logs, err := r.run(s, o)
				if tt.wantError != "" {
					require.ErrorContains(t, err, tt.wantError, logs)
					require.Equal(t, tips, r.tips("B"))
					return
				}
				require.NoError(t, err, logs)
				r.requireStack("main", "B")
				require.Equal(t, []string{"init", "a1", "m1", "b1"}, r.names("B"))
			})
		}
	})

	t.Run("TrunkMergeWithResolution", func(t *testing.T) {
		// The trunk has a merge commit that resolved a conflict, and a layer merged the trunk. The merge commits
		// of the trunk are not replayed, so they are not reported as merge commits the restack drops.
		t.Run("Bottom", func(t *testing.T) {
			r := newStackRepo(t)
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(40, "main", "A", "B")
			// A feature merges main with a conflict resolution, and lands with a merge commit.
			r.branch("F", "main")
			r.file("main", "app.txt", "M\n")
			r.file("F", "app.txt", "F\n")
			r.checkout("F")
			if err := r.tryGit(r.dev, "merge", "--quiet", "--no-edit", "main"); err == nil {
				t.Fatal("expected a conflict in app.txt")
			}
			require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte("FM\n"), 0644))
			r.git(r.dev, "add", "--all")
			r.git(r.dev, "commit", "--quiet", "--no-edit")
			r.checkout("main")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--no-edit", "F")
			r.push("main")
			r.land(s)
			// B merges main, and resolves the atlas.sum conflict by rehashing.
			r.git(r.dev, "fetch", "--quiet", "origin")
			r.git(r.dev, "checkout", "--quiet", "-B", "B", "origin/B")
			if err := r.tryGit(r.dev, "merge", "--quiet", "--no-edit", "origin/main"); err == nil {
				t.Log("no atlas.sum conflict")
			} else {
				r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
				r.git(r.dev, "add", "--all")
				r.git(r.dev, "commit", "--quiet", "--no-edit")
			}
			r.push("B")
			r.git(r.dev, "checkout", "--quiet", "-B", "main", "origin/main")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			act, logs, err := r.run(s, run{branch: "main"})
			require.NoError(t, err, logs)
			require.Equal(t, "true", act.output["rebased"])
			r.requireStack("main", "B")
			require.Equal(t, []string{"init", "a1", "m1", "b1"}, r.names("B"))
		})
		t.Run("Upper", func(t *testing.T) {
			r := newStackRepo(t)
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(43, "main", "A", "B")
			r.branch("F", "main")
			r.file("main", "app.txt", "M\n")
			r.file("F", "app.txt", "F\n")
			r.checkout("F")
			if err := r.tryGit(r.dev, "merge", "--quiet", "--no-edit", "main"); err == nil {
				t.Fatal("expected a conflict in app.txt")
			}
			require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte("FM\n"), 0644))
			r.git(r.dev, "add", "--all")
			r.git(r.dev, "commit", "--quiet", "--no-edit")
			r.checkout("main")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--no-edit", "F")
			r.push("main")
			// The developer of B merges main into it.
			r.checkout("B")
			r.git(r.dev, "merge", "--quiet", "--no-edit", "main")
			r.push("B")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			act, logs, err := r.run(s, run{branch: "main"})
			require.NoError(t, err, logs)
			require.Equal(t, "true", act.output["rebased"])
			r.requireStack("main", "A", "B")

		})
	})

	t.Run("MergeResolvedWithHash", func(t *testing.T) {
		// The developer merges the trunk into A, and resolves the atlas.sum conflict by rehashing. The merge
		// has no changes of its own other than atlas.sum, which the restack generates again.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(30, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// The developer merges main into A, and resolves the atlas.sum conflict by rehashing.
		r.checkout("A")
		r.git(r.dev, "fetch", "--quiet", "origin")
		if err := r.tryGit(r.dev, "merge", "--quiet", "--no-edit", "origin/main"); err == nil {
			t.Fatal("expected an atlas.sum conflict")
		}
		r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
		r.git(r.dev, "add", "--all")
		r.git(r.dev, "commit", "--quiet", "--no-edit")
		r.push("A")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A")
		require.Equal(t, []string{"init", "m1", "a1"}, r.names("A"))

	})

	t.Run("CherryPickedMigration", func(t *testing.T) {
		// B cherry-picked a migration commit of A. Replaying B from the head of A skips the cherry-pick,
		// which would otherwise leave B with a copy of the migration of A.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.file("B", "b.txt", "b\n")
		r.migration("A", "20260102000100", "a2")
		a2 := r.gitOut(r.dev, "rev-parse", "A")
		r.checkout("B")
		r.git(r.dev, "cherry-pick", a2)
		r.push("A", "B")
		s := r.stack(32, "main", "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"init", "m1", "a1", "a2"}, r.names("B"))

	})

	t.Run("SumIgnoreDuplicate", func(t *testing.T) {
		// A file excluded from atlas.sum has the version of a trunk migration, and still counts as a duplicate.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.checkout("A")
		dir := filepath.Join(r.dev, r.dir)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "20260105000000_a1.sql"), []byte("-- atlas:sum ignore\nCREATE TABLE a1 (id int);\n"), 0644))
		r.git(r.dev, "add", "--all")
		r.git(r.dev, "commit", "--quiet", "--message", "A: add a1 (sum ignore)")
		r.migration("A", "20260102000000", "a2")
		r.push("A")
		s := r.stack(44, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A")

	})

	t.Run("NoOwnCommits", func(t *testing.T) {
		// B has no commits of its own, and is restacked onto the new head of A.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.push("A", "B")
		s := r.stack(41, "main", "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")

	})

	t.Run("SingleOutput", func(t *testing.T) {
		// A has a pull request to main that is not stacked, and is rebased as without the input. The stack on A
		// does not need a restack, and the output of the rebase of A is kept.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.file("B", "b.txt", "b\n")
		r.push("A", "B")
		s := r.stackAt(42, 2, "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "A", prs: []*atlasaction.PullRequest{{Number: 1, Ref: "A", Base: "main"}}})
		require.NoError(t, err, logs)
		require.Contains(t, logs, "Migrations rebased successfully")
		require.Equal(t, "true", act.output["rebased"], logs)

	})

	t.Run("HeldHeadsNoActivity", func(t *testing.T) {
		// Without the activity, a layer is restacked when it is built on a head known without it.
		t.Run("MovedByThisRun", func(t *testing.T) {
			// ui is based on auth, which this run restacks. The head auth had before is known from the run.
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
				r.stackAt(9, 11, "auth", "ui"),
			}
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			act, logs, err := r.run(nil, run{branch: "main", stacks: stacks, unreadable: []int{1}})
			require.NoError(t, err, logs)
			require.Equal(t, "true", act.output["rebased"])
			r.requireStack("main", "auth", "api")
			r.requireStack("auth", "ui")
			require.Equal(t, []string{"ui: add ui1", botMessage}, r.log("auth", "ui"))
		})
		t.Run("MergedHead", func(t *testing.T) {
			// B is built on the last head of the merged A, which GitHub keeps as refs/pull/N/head.
			r := newStackRepo(t)
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(3, "main", "A", "B")
			r.land(s)
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			act, logs, err := r.run(s, run{branch: "B", noHistory: true})
			require.NoError(t, err, logs)
			require.Equal(t, "true", act.output["rebased"])
			r.requireStack("main", "B")
			require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"))
		})
	})

	t.Run("RewordedThenEditedBelow", func(t *testing.T) {
		// A rewords its commit, which keeps its patch, and then edits the migration again. B has the
		// reworded commit's patch, so it is not reported as a commit A no longer has.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(40, "main", "A", "B")
		r.amend("A", "A: create table a1")
		r.file("A", "migrations/20260102000000_a1.sql", "CREATE TABLE a1 (id bigint);\n")
		r.hash("A")
		r.forcePush("A")
		act, logs, err := r.run(s, run{branch: "A"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
	})

	t.Run("BotCommitNotLast", func(t *testing.T) {
		// A later run amends the commit of the action only while it is the last commit of the branch.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(30, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, []string{"A: add a1", botMessage}, r.log("main", "A"))
		// The developer adds a commit on top of the commit of the action.
		r.git(r.dev, "fetch", "--quiet", "origin")
		r.git(r.dev, "checkout", "--quiet", "-B", "A", "origin/A")
		r.file("A", "app.txt", "A\n")
		r.push("A")
		r.migration("main", "20260106000000", "m2")
		r.push("main")
		_, logs, err = r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A")
		require.Equal(t, []string{"A: add a1", botMessage, "A: app.txt", botMessage}, r.log("main", "A"))
	})

	t.Run("TrunkRewrittenThenPushed", func(t *testing.T) {
		// develop has no pull request, and is force-pushed to drop y.txt, which A has. The run of that push refuses,
		// and so does the run of the next push, which reads the force-pushes of develop.
		r := newStackRepo(t)
		r.branch("develop", "main")
		r.checkout("develop")
		r.file("develop", "x.txt", "x\n")
		r.file("develop", "y.txt", "y\n")
		r.git(r.dev, "reset", "--quiet", "--soft", "main")
		r.git(r.dev, "commit", "--quiet", "--message", "develop: add x and y")
		r.branch("A", "develop")
		r.migration("A", "20260102000000", "a1")
		r.push("develop", "A")
		s := r.stack(15, "develop", "A")
		before := r.tip("develop")
		r.checkout("develop")
		r.git(r.dev, "reset", "--quiet", "--hard", "main")
		r.file("develop", "x.txt", "x\n")
		r.migration("develop", time.Now().UTC().Format("20060102150405"), "d1")
		r.forcePush("develop")

		tips := r.tips("A")
		_, logs, err := r.run(s, run{branch: "develop", before: before})
		require.Error(t, err, logs)
		require.Regexp(t, "it has commits that develop no longer has, which a restack would drop: [0-9a-f]{7} \\(develop: add x and y\\)", err.Error())
		require.Equal(t, tips, r.tips("A"), "nothing is pushed")

		// One more fast-forward push to develop.
		before = r.tip("develop")
		r.file("develop", "z.txt", "z\n")
		r.push("develop")
		_, logs, err = r.run(s, run{branch: "develop", before: before})
		require.Error(t, err, logs)
		require.Regexp(t, "it has commits that develop no longer has, which a restack would drop: [0-9a-f]{7} \\(develop: add x and y\\)", err.Error())
		require.Equal(t, tips, r.tips("A"), "the second run also refuses")
	})

	t.Run("StaleForcePush", func(t *testing.T) {
		// The developer force-pushes A from a copy without the restack of the action. B has a commit of the trunk
		// that A no longer has, which is not reported as dropped, as the trunk has it.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(10, "main", "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		r.requireStack("main", "A", "B")
		before := r.tip("A")
		// The local A of the developer is the one before the restack.
		r.file("A", "app.txt", "x\n")
		r.forcePush("A")
		act, logs, err := r.run(s, run{branch: "A", before: before})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"), "only the commits of B are replayed")
		require.Equal(t, []string{"A: add a1", "A: app.txt", botMessage}, r.log("main", "A"))
	})

	t.Run("SingleThenStack", func(t *testing.T) {
		// A has a pull request to main that is not stacked, and the stack on A is rebuilt on its new head. The rebase
		// of A moves it, and B is restacked from the head A had, even without the activity of A.
		for _, activity := range []string{"", "none"} {
			t.Run("activity="+activity, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stackAt(42, 2, "A", "B")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				// A gets a commit, and B is rebuilt on it. Both are pushed, as gh stack push does.
				before := r.tip("A")
				r.file("A", "app.txt", "x\n")
				r.checkout("B")
				r.git(r.dev, "rebase", "--quiet", "A")
				r.push("A")
				r.forcePush("B")
				act, logs, err := r.run(s, run{branch: "A", before: before, activity: activity, prs: []*atlasaction.PullRequest{{Number: 1, Ref: "A", Base: "main"}}})
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"], logs)
				r.requireStack("A", "B")
			})
		}
	})

	t.Run("TrunkForcePushes", func(t *testing.T) {
		// develop has no pull request, and was force-pushed to drop y.txt, which A has. A later push to develop
		// triggers the run, which reads the force-pushes of develop to find the head A was built on.
		t.Run("Fetched", func(t *testing.T) {
			for _, pruned := range []bool{false, true} {
				t.Run(map[bool]string{false: "AllFetchable", true: "OneOldHeadPruned"}[pruned], func(t *testing.T) {
					r := newStackRepo(t)
					r.branch("develop", "main")
					r.checkout("develop")
					r.file("develop", "x.txt", "x\n")
					r.file("develop", "y.txt", "y\n")
					r.git(r.dev, "reset", "--quiet", "--soft", "main")
					r.git(r.dev, "commit", "--quiet", "--message", "develop: add x and y")
					r.branch("A", "develop")
					r.migration("A", "20260102000000", "a1")
					// develop moves on, so its head before the force-push is not in A.
					r.file("develop", "z.txt", "z\n")
					r.push("develop", "A")
					s := r.stack(15, "develop", "A")
					r.checkout("develop")
					r.git(r.dev, "reset", "--quiet", "--hard", "main")
					r.file("develop", "x.txt", "x\n")
					r.migration("develop", time.Now().UTC().Format("20060102150405"), "d1")
					r.forcePush("develop")
					// A later fast-forward push to develop triggers the run.
					before := r.tip("develop")
					r.file("develop", "w.txt", "w\n")
					r.push("develop")
					tips := r.tips("A")
					o := run{branch: "develop", before: before}
					if pruned {
						o.scm = func(s *stackSCM) atlasaction.SCMClient { return &prunedForcePushes{stackSCM: s, branch: "develop"} }
					}
					_, logs, err := r.run(s, o)
					require.Error(t, err, logs)
					require.Regexp(t, "it has commits that develop no longer has, which a restack would drop: [0-9a-f]{7} \\(develop: add x and y\\)", err.Error())
					require.Equal(t, tips, r.tips("A"), "nothing is pushed")
				})
			}
		})
		t.Run("Unreadable", func(t *testing.T) {
			r := newStackRepo(t)
			r.branch("develop", "main")
			r.checkout("develop")
			r.file("develop", "x.txt", "x\n")
			r.file("develop", "y.txt", "y\n")
			r.git(r.dev, "reset", "--quiet", "--soft", "main")
			r.git(r.dev, "commit", "--quiet", "--message", "develop: add x and y")
			r.branch("A", "develop")
			r.migration("A", "20260102000000", "a1")
			r.push("develop", "A")
			s := r.stack(15, "develop", "A")
			r.checkout("develop")
			r.git(r.dev, "reset", "--quiet", "--hard", "main")
			r.file("develop", "x.txt", "x\n")
			r.migration("develop", time.Now().UTC().Format("20060102150405"), "d1")
			r.forcePush("develop")
			// One more fast-forward push to develop, whose run cannot read the activity.
			before := r.tip("develop")
			r.file("develop", "z.txt", "z\n")
			r.push("develop")
			tips := r.tips("A")
			_, logs, err := r.run(s, run{branch: "develop", before: before, activity: "error"})
			require.ErrorContains(t, err, `#1 (A): the force-pushes of branch "develop" cannot be read. It is not built on the latest head of develop`, logs)
			require.Equal(t, tips, r.tips("A"), "nothing is pushed")
		})
	})

	t.Run("RemovesParentMigration", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.checkout("A")
		r.git(r.dev, "rm", "--quiet", "migrations/20260101000000_init.sql")
		r.git(r.dev, "commit", "--quiet", "--message", "A: drop init")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(40, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		tips := r.tips("A")
		_, logs, err := r.run(s, run{branch: "main"})
		require.ErrorContains(t, err, "it removes migration files of main: 20260101000000_init.sql", logs)
		require.Equal(t, tips, r.tips("A"))
	})

	t.Run("AttributesRestored", func(t *testing.T) {
		// The repository has attributes of its own, which the merge driver of the restack is added to for the run.
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(55, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		attrs := filepath.Join(r.ci, ".git", "info", "attributes")
		require.NoError(t, os.MkdirAll(filepath.Dir(attrs), 0755))
		require.NoError(t, os.WriteFile(attrs, []byte("*.bin binary\n"), 0644))
		_, logs, err := r.run(s, run{branch: "main", keep: true})
		require.NoError(t, err, logs)
		r.requireStack("main", "A")
		b, err := os.ReadFile(attrs)
		require.NoError(t, err)
		require.Equal(t, "*.bin binary\n", string(b))
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
		require.ErrorContains(t, err, `stack #9: #11 (ui): the activity of branch "auth" cannot be read. It is not built on the latest head of auth, so its commits cannot be told from the ones auth had. Nothing was pushed`, logs)
		require.ErrorContains(t, err, `stack #5: #2 (api): the activity of branch "auth" cannot be read. It is not built on the latest head of #1 (auth), so its commits cannot be told from the ones #1 (auth) had. Nothing was pushed`)
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

	// The git configuration of the CI machine, like a self-hosted runner, does not change the restack.
	t.Run("GitConfig", func(t *testing.T) {
		t.Run("UpdateRefs", func(t *testing.T) {
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
			old := r.tip("B")
			act, logs, err := r.run(s, run{branch: "B", exec: racer, config: []string{"rebase.updateRefs=true"}})
			require.True(t, raced)
			require.NoError(t, err, logs)
			require.Contains(t, logs, "nothing was pushed")
			require.Equal(t, "false", act.output["rebased"])
			require.Equal(t, old, r.gitOut(r.ci, "rev-parse", "refs/heads/B"), "nothing was pushed, so B stays at the head the run started on")
		})
		t.Run("RebaseMerges", func(t *testing.T) {
			r := newStackRepo(t)
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.file("A", "feature.txt", "A\n")
			// B is built a second after A, as in a real stack.
			time.Sleep(1100 * time.Millisecond)
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(43, "main", "A", "B")
			r.file("main", "other.txt", "o\n")
			r.push("main")
			r.checkout("B")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--message", "Merge main into B", "main")
			r.push("B")
			r.land(s)
			r.checkout("main")
			r.git(r.dev, "rm", "--quiet", "feature.txt", "other.txt")
			r.git(r.dev, "commit", "--quiet", "--message", "main: revert feature and other")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			_, logs, err := r.run(s, run{branch: "main", config: []string{"rebase.rebaseMerges=true"}})
			require.NoError(t, err, logs)
			for _, f := range []string{"feature.txt", "other.txt"} {
				require.Empty(t, r.gitOut(r.origin, "ls-tree", "B", "--", f), "%s, which main removed, is not brought back to B", f)
			}
		})
		t.Run("DiffRelative", func(t *testing.T) {
			r := newStackRepo(t, "db/migrations")
			r.file("main", "app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.push("A")
			s := r.stack(17, "main", "A")
			r.file("main", "app.txt", "v2\n")
			r.push("main")
			// The developer merges the trunk into A, and fixes a file outside the working directory in the merge.
			r.checkout("A")
			r.git(r.dev, "merge", "--quiet", "--no-ff", "--no-commit", "main")
			require.NoError(t, os.WriteFile(filepath.Join(r.dev, "fix.txt"), []byte("fix\n"), 0644))
			r.git(r.dev, "add", "fix.txt")
			r.git(r.dev, "commit", "--quiet", "--message", "Merge main into A")
			r.push("A")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			tips := r.tips("A")
			_, logs, err := r.run(s, run{branch: "main", wd: "db", inputs: map[string]string{"dir": "file://migrations"}, config: []string{"diff.relative=true"}})
			require.Error(t, err, logs)
			require.Regexp(t, `merge commit [0-9a-f]{7} has changes of its own in fix\.txt`, err.Error())
			require.Equal(t, tips, r.tips("A"))
		})
		t.Run("DiffRelativeOutside", func(t *testing.T) {
			// The migration directory is outside the working directory, whose changes alone diff.relative shows.
			r := newStackRepo(t, "db/migrations")
			r.file("main", "app/app.txt", "v1\n")
			r.push("main")
			r.branch("A", "main")
			r.migration("A", "20260102000000", "a1")
			r.branch("B", "A")
			r.migration("B", "20260103000000", "b1")
			r.push("A", "B")
			s := r.stack(18, "main", "A", "B")
			r.migration("main", "20260105000000", "m1")
			r.push("main")
			act, logs, err := r.run(s, run{branch: "main", wd: "app", inputs: map[string]string{"dir": "file://../db/migrations"}, config: []string{"diff.relative=true"}})
			require.NoError(t, err, logs)
			require.Equal(t, "true", act.output["rebased"], logs)
			r.requireStack("main", "A", "B")
		})
	})

	t.Run("HeadsBelowApart", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			old       bool   // Whether B is built on the old head of A and merges the new one, or the other way around.
			reword    bool   // Whether A is only reworded, and keeps its migration.
			merged    bool   // Whether A lands before the restack, and B becomes the bottom layer.
			wantError string // Regular expression, empty if the restack succeeds.
		}{
			{name: "MergesNew", old: true, wantError: `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add a1\)\. Nothing was pushed`},
			{name: "MergesOld", wantError: `#2 \(B\): it has commits that #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add a1\)\. Nothing was pushed`},
			{name: "MergesOldThenLanded", merged: true, wantError: `#2 \(B\): it has commits that the merged #1 \(A\) no longer has, which a restack would drop: [0-9a-f]{7} \(A: add a1\)\. Nothing was pushed`},
			// Like a git pull of B after A was reworded and force-pushed: the commits of both heads of A are A's.
			{name: "Reworded", old: true, reword: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.push("A")
				old := r.tip("A")
				if tt.old {
					r.branch("B", "A")
					r.migration("B", "20260103000000", "b1")
				}
				if tt.reword {
					r.amend("A", "A: add a1 (reworded)")
				} else {
					// A drops a1, and gets a2 instead.
					r.checkout("A")
					r.git(r.dev, "reset", "--quiet", "--hard", "main")
					r.migration("A", "20260102000100", "a2")
				}
				r.forcePush("A")
				merge := old
				if tt.old {
					merge = r.tip("A")
				} else {
					r.branch("B", "A")
					r.migration("B", "20260103000000", "b1")
				}
				// B merges the other head of A, so it has both.
				r.checkout("B")
				require.Error(t, r.tryGit(r.dev, "merge", "--quiet", "--no-ff", "--no-edit", merge), "atlas.sum conflicts")
				r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
				r.git(r.dev, "add", "--all")
				r.git(r.dev, "commit", "--quiet", "--no-edit")
				r.push("B")
				s := r.stack(43, "main", "A", "B")
				if tt.merged {
					r.land(s)
				}
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("B")
				act, logs, err := r.run(s, run{branch: "main"})
				if tt.wantError != "" {
					// Whichever head of A the walk finds first, the commit A dropped is named, and nothing is pushed.
					require.Error(t, err, logs)
					require.Regexp(t, tt.wantError, err.Error())
					require.Equal(t, "false", act.output["rebased"])
					require.Equal(t, tips, r.tips("B"))
					return
				}
				require.NoError(t, err, logs)
				r.requireStack("main", "A", "B")
				require.Equal(t, []string{"init", "m1", "a1", "b1"}, r.names("B"), "a1 is not copied")
				require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"), "only the commits of B are replayed")
			})
		}
	})

	t.Run("BottomBase", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("develop", "main")
		r.file("develop", "dev.txt", "dev\n")
		r.push("develop")
		r.branch("A", "develop")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(44, "main", "A")
		// The pull request targets another branch than the base of its stack.
		s.Layers[0].Base = "develop"
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		tips := r.tips("A")
		_, logs, err := r.run(s, run{branch: "A"})
		require.EqualError(t, err, `stack #44: pull request #1 targets "develop", not the base of the stack "main"`, logs)
		require.Equal(t, tips, r.tips("A"), "the commits of develop are not replayed into A")
	})

	t.Run("MergedBelowNotRetargeted", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(46, "main", "A", "B")
		r.migration("A", time.Now().UTC().Format("20060102150405"), "a2")
		r.push("A")
		r.land(s)
		// The run starts before GitHub retargets B onto the base of the stack.
		s.Layers[1].Base = "A"
		act, logs, err := r.run(s, run{branch: "B"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "B")
		require.Equal(t, []string{"init", "a1", "a2", "b1"}, r.names("B"))
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("main", "B"))
	})

	t.Run("UnrelatedHistory", func(t *testing.T) {
		r := newStackRepo(t)
		r.git(r.dev, "checkout", "--quiet", "--orphan", "A")
		r.git(r.dev, "commit", "--quiet", "--message", "A: root")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(45, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		_, logs, err := r.run(s, run{branch: "main"})
		require.EqualError(t, err, "stack #45: #1 (A): no commit in common with main", logs)
	})

	t.Run("OtherFormats", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			format    string
			trunk     []string // Files of the trunk, then the file it gets.
			layer     string   // File of the layer.
			layerSQL  string   // Contents of the file of the layer, if it edits a file of the trunk.
			move      string   // File the trunk gets, or a file outside the migration directory if empty.
			moveSQL   string   // Contents of the file the trunk gets, if it edits one of its files.
			want      []string // Files of the layer after the run, if it is restacked.
			wantError string
		}{
			// golang-migrate versions are sequential, and a restack cannot renumber them.
			{name: "GolangMigrateSameVersion", format: "golang-migrate", trunk: []string{"1_init.up.sql"}, layer: "2_z.up.sql", move: "2_m.up.sql",
				wantError: "#1 (A): its migrations must be moved on top of the ones of main, which `atlas migrate rebase` supports only for the \"atlas\" format"},
			// The repeatable migrations of Flyway sort after the versioned ones in atlas.sum, and have no version.
			{name: "FlywayRepeatable", format: "flyway", trunk: []string{"V1__init.sql", "R__view.sql"}, layer: "V3__a.sql", move: "V2__m.sql",
				want: []string{"R__view.sql", "V1__init.sql", "V2__m.sql", "V3__a.sql", "atlas.sum"}},
			{name: "FlywayRepeatableNoMove", format: "flyway", trunk: []string{"V1__init.sql", "R__view.sql"}, layer: "V3__a.sql"},
			{name: "FlywayRepeatableOnly", format: "flyway", trunk: []string{"V1__init.sql", "R__a.sql"}, layer: "R__b.sql", move: "V2__m.sql",
				want: []string{"R__a.sql", "R__b.sql", "V1__init.sql", "V2__m.sql", "atlas.sum"}},
			// The trunk changes its repeatable migrations, which the sums of the layer do not show, but conflict in atlas.sum.
			{name: "FlywayTrunkEditsRepeatable", format: "flyway", trunk: []string{"V1__init.sql", "R__view.sql"}, layer: "V3__a.sql", move: "R__view.sql", moveSQL: "SELECT 2;\n",
				want: []string{"R__view.sql", "V1__init.sql", "V3__a.sql", "atlas.sum"}},
			{name: "FlywayTrunkAddsRepeatable", format: "flyway", trunk: []string{"V1__init.sql", "R__view.sql"}, layer: "V3__a.sql", move: "R__other.sql",
				want: []string{"R__other.sql", "R__view.sql", "V1__init.sql", "V3__a.sql", "atlas.sum"}},
			// Flyway versions separate their parts with dots or underscores, and compare their numbers.
			{name: "FlywaySubVersion", format: "flyway", trunk: []string{"V2__init.sql"}, layer: "V2_1__a.sql"},
			{name: "FlywayDateVersion", format: "flyway", trunk: []string{"V2026_01_01__init.sql"}, layer: "V2026_01_02__a.sql"},
			{name: "FlywaySortedVersions", format: "flyway", trunk: []string{"V2__init.sql"}, layer: "V2_2__a.sql", move: "V2_1__m.sql",
				want: []string{"V2_1__m.sql", "V2_2__a.sql", "V2__init.sql", "atlas.sum"}},
			{name: "FlywaySameVersion", format: "flyway", trunk: []string{"V2__init.sql"}, layer: "V2_1__a.sql", move: "V2.1__m.sql",
				wantError: "#1 (A): its migrations must be moved on top of the ones of main, which `atlas migrate rebase` supports only for the \"atlas\" format"},
			// Flyway drops the trailing zero parts of a version: V2.0 and V2_0 are V2.
			{name: "FlywayTrailingZeroDot", format: "flyway", trunk: []string{"V1__init.sql"}, layer: "V2.0__a.sql", move: "V2__m.sql",
				wantError: "#1 (A): its migrations must be moved on top of the ones of main, which `atlas migrate rebase` supports only for the \"atlas\" format"},
			{name: "FlywayTrailingZeroUnderscore", format: "flyway", trunk: []string{"V1__init.sql"}, layer: "V2_0__a.sql", move: "V2__m.sql",
				wantError: "#1 (A): its migrations must be moved on top of the ones of main, which `atlas migrate rebase` supports only for the \"atlas\" format"},
			// Flyway also reads the files of subdirectories.
			{name: "FlywaySubdirRepeatable", format: "flyway", trunk: []string{"V1__init.sql", "views/R__view.sql"}, layer: "V3__a.sql", move: "views/R__view.sql", moveSQL: "SELECT 2;\n",
				want: []string{"V1__init.sql", "V3__a.sql", "atlas.sum", "views"}},
			{name: "FlywayDigitSubdir", format: "flyway", trunk: []string{"V1__init.sql", "v2/R__view.sql"}, layer: "V3__a.sql", move: "V2__m.sql",
				want: []string{"V1__init.sql", "V2__m.sql", "V3__a.sql", "atlas.sum", "v2"}},
			// golang-migrate reads only the files at the top of the directory.
			{name: "GolangMigrateSubdirUntouched", format: "golang-migrate", trunk: []string{"1_init.up.sql", "seeds/x.sql"}, layer: "2_a.up.sql", move: "seeds/x.sql", moveSQL: "SELECT 2;\n"},
			{name: "FlywayBothEditRepeatables", format: "flyway", trunk: []string{"V1__init.sql", "R__a.sql", "R__b.sql"}, layer: "R__a.sql", layerSQL: "SELECT 2;\n", move: "R__b.sql", moveSQL: "SELECT 3;\n",
				want: []string{"R__a.sql", "R__b.sql", "V1__init.sql", "atlas.sum"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.checkout("main")
				r.git(r.dev, "rm", "--quiet", r.dir+"/20260101000000_init.sql")
				for _, f := range tt.trunk {
					r.formatFile("main", tt.format, f)
				}
				r.push("main")
				r.branch("A", "main")
				r.formatFile("A", tt.format, tt.layer, cmp.Or(tt.layerSQL, fmt.Sprintf("-- %s\nSELECT 1;\n", tt.layer)))
				r.push("A")
				s := r.stack(60, "main", "A")
				if tt.move != "" {
					r.formatFile("main", tt.format, tt.move, cmp.Or(tt.moveSQL, fmt.Sprintf("-- %s\nSELECT 1;\n", tt.move)))
				} else {
					r.file("main", "app.txt", "main\n")
				}
				r.push("main")
				tips := r.tips("A")
				act, logs, err := r.run(s, run{branch: "main", inputs: map[string]string{"dir": "file://" + r.dir + "?format=" + tt.format}})
				switch {
				case tt.wantError != "":
					require.ErrorContains(t, err, tt.wantError, logs)
					require.Equal(t, tips, r.tips("A"), "nothing is pushed")
				case tt.want == nil:
					require.NoError(t, err, logs)
					require.Equal(t, "false", act.output["rebased"])
					require.Equal(t, tips, r.tips("A"))
				default:
					require.NoError(t, err, logs)
					require.Equal(t, "true", act.output["rebased"])
					r.git(r.origin, "merge-base", "--is-ancestor", "main", "A")
					require.Equal(t, tt.want, strings.Fields(r.gitOut(r.origin, "ls-tree", "--name-only", "A:"+r.dir)), "the files keep their names")
					dir := filepath.Join(t.TempDir(), "A")
					r.git("", "clone", "--quiet", "--branch", "A", r.origin, dir)
					r.atlas(dir, "migrate", "validate", "--dir", "file://"+r.dir+"?format="+tt.format)
				}
			})
		}
	})

	t.Run("FlywayAtRoot", func(t *testing.T) {
		r := newStackRepo(t, ".")
		r.checkout("main")
		r.git(r.dev, "rm", "--quiet", "20260101000000_init.sql", "atlas.sum")
		r.git(r.dev, "commit", "--quiet", "--message", "rm")
		r.formatFile("main", "flyway", "V1__init.sql")
		r.formatFile("main", "flyway", "R__view.sql")
		r.push("main")
		r.branch("A", "main")
		r.formatFile("A", "flyway", "V3__a.sql")
		r.push("A")
		s := r.stack(67, "main", "A")
		r.formatFile("main", "flyway", "R__view.sql", "SELECT 2;\n")
		r.push("main")
		act, logs, err := r.run(s, run{branch: "main", inputs: map[string]string{"dir": "file://.?format=flyway"}})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		require.Equal(t, []string{"R__view.sql", "V1__init.sql", "V3__a.sql", "atlas.sum"}, strings.Fields(r.gitOut(r.origin, "ls-tree", "--name-only", "A")))
	})

	t.Run("RemovedBelow", func(t *testing.T) {
		for _, tt := range []struct {
			name      string
			reword    bool // Whether A is also rewritten, and stays open.
			rewind    bool // Whether A drops a2 by a rewrite instead of a new commit.
			land      bool // Whether A lands before the restack.
			wantError string
		}{
			// A removes a2 with a new commit, and lands while the run of the push waits in the queue.
			{name: "ThenLanded", land: true},
			// A is reworded and removes a2 with a new commit: B is not built on the head A has.
			{name: "Reworded", reword: true},
			// A drops a2 by a rewrite and lands. Without the activity, nothing tells B's a2 from its own commits, and
			// the quick check finds B up to date: nothing is pushed, like the full check, which refuses.
			{name: "RewoundThenLanded", rewind: true, land: true, wantError: "-"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				r.migration("A", "20260102000100", "a2")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(68, "main", "A", "B")
				before := r.tip("A")
				r.checkout("A")
				switch {
				case tt.rewind:
					r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
					r.amend("A", "A: add a1 (reworded)")
				case tt.reword:
					a2 := r.gitOut(r.dev, "rev-parse", "HEAD")
					r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~2")
					r.git(r.dev, "cherry-pick", a2+"~1")
					r.amend("A", "A: add a1 (reworded)")
					r.git(r.dev, "cherry-pick", a2)
					fallthrough
				default:
					r.git(r.dev, "rm", "--quiet", r.dir+"/20260102000100_a2.sql")
					r.hash("A")
				}
				r.forcePush("A")
				o := run{branch: "A", before: before}
				if tt.land {
					r.land(s)
					o = run{branch: "main"}
				}
				tips := r.tips("B")
				act, logs, err := r.run(s, o)
				switch tt.wantError {
				case "":
				case "-":
					require.Equal(t, tips, r.tips("B"), "nothing is pushed", logs)
					return
				default:
					require.ErrorContains(t, err, tt.wantError, logs)
					require.Equal(t, tips, r.tips("B"))
					return
				}
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"], "B no longer has a2, which conflicts with A or main")
				require.Equal(t, []string{"init", "a1", "b1"}, r.names("B"))
			})
		}
	})

	t.Run("MergeRenamesTrunkMigration", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.push("A")
		s := r.stack(64, "main", "A")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// The developer merges main into A, and renames the migration of main in the merge, which a restack would drop.
		r.checkout("A")
		require.Error(t, r.tryGit(r.dev, "merge", "--quiet", "--no-ff", "--no-commit", "main"), "atlas.sum conflicts")
		r.git(r.dev, "mv", r.dir+"/20260105000000_m1.sql", r.dir+"/20260106000000_m1.sql")
		r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
		r.git(r.dev, "add", "--all")
		r.git(r.dev, "commit", "--quiet", "--no-edit")
		r.push("A")
		r.migration("main", "20260107000000", "m2")
		r.push("main")
		tips := r.tips("A")
		_, logs, err := r.run(s, run{branch: "main"})
		require.Error(t, err, logs)
		require.Regexp(t, `#1 \(A\): merge commit [0-9a-f]{7} has changes of its own in migrations/20260106000000_m1\.sql, which a restack drops\. Nothing was pushed`, err.Error())
		require.Equal(t, tips, r.tips("A"))
	})

	t.Run("ParentRemovesMigration", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.migration("A", "20260102000100", "a2")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(61, "main", "A", "B")
		// A removes a2 with a new commit. B still has it, and conflicts with A in atlas.sum.
		before := r.tip("A")
		r.checkout("A")
		r.git(r.dev, "rm", "--quiet", r.dir+"/20260102000100_a2.sql")
		r.hash("A")
		r.push("A")
		act, logs, err := r.run(s, run{branch: "A", before: before})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "A", "B")
		require.Equal(t, []string{"init", "a1", "b1"}, r.names("B"))
		require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
	})

	t.Run("SingleMergeThenStacked", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("X", "main")
		r.migration("X", "20260102000000", "x1")
		r.push("X")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// X is not stacked yet: the action merges main into X, and moves x1 on top in the merge commit.
		before := r.tip("X")
		r.file("X", "app.txt", "x\n")
		r.push("X")
		act, logs, err := r.run(nil, run{branch: "X", before: before, prs: []*atlasaction.PullRequest{{Number: 1, Ref: "X", Base: "main"}}})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"], logs)
		// Y is stacked on X, and main moves again. The merge commit of the action only renamed x1.
		r.git(r.dev, "fetch", "--quiet", "origin")
		r.git(r.dev, "checkout", "--quiet", "-B", "X", "origin/X")
		r.branch("Y", "X")
		r.migration("Y", "20260106000000", "y1")
		r.push("Y")
		s := r.stack(62, "main", "X", "Y")
		r.migration("main", "20260107000000", "m2")
		r.push("main")
		act, logs, err = r.run(s, run{branch: "main"})
		require.NoError(t, err, logs)
		require.Equal(t, "true", act.output["rebased"])
		r.requireStack("main", "X", "Y")
		require.Equal(t, []string{"init", "m1", "m2", "x1", "y1"}, r.names("Y"))
	})

	t.Run("HeadAsOption", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.migration("A", "20260102000000", "a1")
		r.branch("B", "A")
		r.migration("B", "20260103000000", "b1")
		r.push("A", "B")
		s := r.stack(63, "main", "A", "B")
		r.migration("main", "20260105000000", "m1")
		r.push("main")
		// A head of the activity that is not a commit must not reach git as an option. The run fails, and pushes nothing.
		marker := filepath.Join(t.TempDir(), "marker")
		tips := r.tips("A", "B")
		_, logs, err := r.run(s, run{branch: "main", scm: func(s *stackSCM) atlasaction.SCMClient {
			return &extraHead{stackSCM: s, head: "--upload-pack=touch " + marker + ";"}
		}})
		require.Error(t, err, logs)
		require.Equal(t, tips, r.tips("A", "B"))
		_, serr := os.Stat(marker)
		require.ErrorIs(t, serr, os.ErrNotExist, "the head did not run as a command")
	})

	t.Run("SameSubjectSplit", func(t *testing.T) {
		for _, amend := range []bool{false, true} {
			t.Run(fmt.Sprint("Amend=", amend), func(t *testing.T) {
				r := newStackRepo(t)
				fix := func(content string) {
					r.checkout("A")
					require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte(content), 0644))
					r.git(r.dev, "add", "--all")
					r.git(r.dev, "commit", "--quiet", "--message", "fix")
				}
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				fix("v1\n")
				fix("v2\n")
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(65, "main", "A", "B")
				before := r.tip("A")
				// The second fix moves up to B, which has it: A drops it, and may amend the first one, with the same subject.
				r.checkout("A")
				r.git(r.dev, "reset", "--quiet", "--hard", "HEAD~1")
				if amend {
					require.NoError(t, os.WriteFile(filepath.Join(r.dev, "app.txt"), []byte("v1b\n"), 0644))
					r.git(r.dev, "commit", "--quiet", "--all", "--amend", "--no-edit")
				}
				r.forcePush("A")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				tips := r.tips("A", "B")
				_, logs, err := r.run(s, run{branch: "A", before: before})
				require.ErrorContains(t, err, "#2 (B): it has commits that #1 (A) no longer has", logs)
				require.Equal(t, tips, r.tips("A", "B"), "B keeps the fix A dropped")
				require.Equal(t, "v2", r.gitOut(r.origin, "show", "B:app.txt"))
			})
		}
	})

	t.Run("SquashedBelow", func(t *testing.T) {
		for _, tt := range []struct {
			name    string
			subject string // Subject of the second commit of A.
			moved   bool   // Whether a later commit of A moved a1 to another version, like an earlier restack.
		}{
			{name: "OtherSubject", subject: "A: add column c"},
			{name: "SameSubject", subject: "A: add a1"},
			{name: "MovedBefore", moved: true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				r := newStackRepo(t)
				r.branch("A", "main")
				r.migration("A", "20260102000000", "a1")
				sql := "CREATE TABLE a1 (id int);\n"
				if tt.moved {
					r.git(r.dev, "mv", r.dir+"/20260102000000_a1.sql", r.dir+"/20260102000100_a1.sql")
					r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
					r.git(r.dev, "add", "--all")
					r.git(r.dev, "commit", "--quiet", "--message", botMessage)
				} else {
					sql = "CREATE TABLE a1 (id int, c int);\n"
					r.migrationSQL("A", "20260102000000", "a1", sql)
					r.amend("A", tt.subject)
				}
				r.branch("B", "A")
				r.migration("B", "20260103000000", "b1")
				r.push("A", "B")
				s := r.stack(66, "main", "A", "B")
				before := r.tip("A")
				r.migration("main", "20260105000000", "m1")
				r.push("main")
				// The developer squashes A onto main, with a1 moved on top, as atlas migrate rebase does.
				r.git(r.dev, "fetch", "--quiet", "origin")
				r.checkout("A")
				r.git(r.dev, "reset", "--quiet", "--hard", "origin/main")
				require.NoError(t, os.WriteFile(filepath.Join(r.dev, r.dir, "20260106000000_a1.sql"), []byte(sql), 0644))
				r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
				r.git(r.dev, "add", "--all")
				r.git(r.dev, "commit", "--quiet", "--message", "A: add a1 (squashed)")
				r.forcePush("A")
				act, logs, err := r.run(s, run{branch: "A", before: before})
				require.NoError(t, err, logs)
				require.Equal(t, "true", act.output["rebased"])
				r.requireStack("main", "A", "B")
				require.Equal(t, []string{"B: add b1", botMessage}, r.log("A", "B"))
			})
		}
	})

	t.Run("Errors", func(t *testing.T) {
		r := newStackRepo(t)
		r.branch("A", "main")
		r.push("A")
		s := r.stack(1, "main", "A")
		other := &atlasaction.Stack{Number: 2, Base: "main", Layers: []atlasaction.StackLayer{{Number: 9, Branch: "A", Base: "main", Open: true}}}
		for _, ci := range []atlasexec.TriggerType{atlasexec.TriggerTypeCircleCIOrb, atlasexec.TriggerTypeAzureDevOps, atlasexec.TriggerTypeGitlab, atlasexec.TriggerTypeBitbucket, "TEAMCITY"} {
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

		// The pull requests of a stack are rebased in the order GitHub lists them, from the bottom up.
		r.branch("B", "A")
		r.push("B")
		reversed := r.stack(3, "main", "A", "B")
		slices.Reverse(reversed.Layers)
		_, _, err = r.run(reversed, run{branch: "B"})
		require.EqualError(t, err, `stack #3: pull request #1 targets "main", not the branch of #2 (B) listed below it`)

		// A migration directory reached through a symbolic link is not supported.
		require.NoError(t, os.Symlink("migrations", filepath.Join(r.ci, "linked")))
		_, _, err = r.run(s, run{branch: "A", inputs: map[string]string{"dir": "file://linked"}, keep: true})
		require.ErrorContains(t, err, `restacking does not support a migration directory that is a symbolic link, or under one: "linked"`)
		require.NoError(t, os.Remove(filepath.Join(r.ci, "linked")))

		// A shallow clone cannot be restacked.
		shallow := filepath.Join(t.TempDir(), "shallow")
		r.git("", "clone", "--quiet", "--depth=1", "file://"+r.origin, shallow)
		r.ci = shallow
		_, _, err = r.run(s, run{branch: "A", keep: true})
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
	s, err := c.Stack(t.Context(), 4)
	require.NoError(t, err)
	require.Equal(t, &atlasaction.Stack{Number: 4, Base: "main", Layers: []atlasaction.StackLayer{
		{Number: 1, Branch: "a", Base: "main", Merged: true},
		{Number: 2, Branch: "b", Base: "main", Open: true},
	}}, s)
	// A stack that is not found is nil.
	s, err = c.Stack(t.Context(), 5)
	require.NoError(t, err)
	require.Nil(t, s)
	_, err = c.Stack(t.Context(), 6)
	require.ErrorContains(t, err, "unexpected status code 502")
}

func TestGitHubTriggerContextBefore(t *testing.T) {
	event := filepath.Join(t.TempDir(), "event.json")
	for _, tt := range []struct {
		name, event, payload, want string
	}{
		{name: "Push", event: "push", payload: `{"before":"35f82a7a3a0aee6352956853ca57756256499558","forced":true}`, want: "35f82a7a3a0aee6352956853ca57756256499558"},
		{name: "NewBranch", event: "push", payload: `{"before":"0000000000000000000000000000000000000000"}`},
		{name: "DeletedBranch", event: "push", payload: `{"before":"35f82a7a3a0aee6352956853ca57756256499558","deleted":true}`},
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
			tc, err := atlasaction.NewGitHub(func(k string) string { return env[k] }, io.Discard).GetTriggerContext(t.Context())
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
	}
	// stackSCM is a GitHub client that serves stacks of pull requests. It reads the
	// activity of their branches from the reflog of the remote.
	stackSCM struct {
		mockSCM
		origin      string
		prs         []*atlasaction.PullRequest
		stacks      []*atlasaction.Stack
		activityErr error         // Error of reading the activity of any branch.
		partial     bool          // Whether the activity of the branches does not go back to their creation.
		unreadable  []int         // Pull requests whose branch activity cannot be read.
		reads       int           // Number of the reads of the activity of branches.
		stackErrs   map[int]error // Errors of the stack lookups.
		unstacked   []int         // Stacks that are not found.
		pruned      []string      // Branches whose activity lists a head that the remote no longer has.
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
		noHistory bool // Whether the activity of the branches cannot be read.
		// activity is "none" if the activity of the branches is not available, "error" if reading it
		// fails, like on a rate limit, and "partial" if it does not go back to their creation, like
		// after a retention period.
		activity string
		// Pull requests whose branch activity cannot be read, while the others can.
		unreadable []int
		stackErrs  map[int]error         // Errors of the stack lookups.
		unstacked  []int                 // Stacks that are unstacked after their pull requests were listed.
		ci         atlasexec.TriggerType // The CI platform the action runs on, GitHub Actions by default.
		inputs     map[string]string
		exec       func(context.Context, string, ...string) *exec.Cmd
		wd         string // Working directory, relative to the clone.
		scm        func(*stackSCM) atlasaction.SCMClient
		// keep runs the action in the clone of the earlier runs, which has their objects. By default,
		// each run gets a fresh clone, as actions/checkout makes, without the force-pushed commits.
		keep   bool
		pruned []string // Branches whose activity lists a head that the remote no longer has.
		config []string // Git configuration of the CI machine, as key=value pairs, for the git commands of the action.
	}
)

func newStackRepo(t *testing.T, dir ...string) *stackRepo {
	t.Helper()
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
	r.t.Helper()
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
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		out = exit.Stderr
	}
	require.NoError(r.t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// tryGit runs a git command in the given directory, and returns its error, like a merge that conflicts.
func (r *stackRepo) tryGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// checkout checks out the local branch in the developer clone.
func (r *stackRepo) checkout(branch string) {
	r.t.Helper()
	r.git(r.dev, "checkout", "--quiet", branch)
}

// branch creates a branch in the developer clone.
func (r *stackRepo) branch(name, from string) {
	r.t.Helper()
	r.git(r.dev, "branch", name, from)
}

// file writes a file and commits it to the branch.
func (r *stackRepo) file(branch, name, content string) {
	r.t.Helper()
	r.checkout(branch)
	p := filepath.Join(r.dev, name)
	require.NoError(r.t, os.MkdirAll(filepath.Dir(p), 0755))
	require.NoError(r.t, os.WriteFile(p, []byte(content), 0644))
	r.git(r.dev, "add", "--all")
	r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("%s: %s", branch, filepath.Base(name)))
}

// migration adds a migration file to the branch, and commits it with the updated atlas.sum.
func (r *stackRepo) migration(branch, version, name string) {
	r.t.Helper()
	r.migrationSQL(branch, version, name, fmt.Sprintf("CREATE TABLE %s (id int);\n", name))
}

// migrationSQL adds a migration file with the given contents to the branch, and commits it with the updated atlas.sum.
func (r *stackRepo) migrationSQL(branch, version, name, sql string) {
	r.t.Helper()
	r.checkout(branch)
	dir := filepath.Join(r.dev, r.dir)
	require.NoError(r.t, os.MkdirAll(dir, 0755))
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, version+"_"+name+".sql"), []byte(sql), 0644))
	r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
	r.git(r.dev, "add", "--all")
	r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("%s: add %s", branch, name))
}

// formatFile writes a migration file of another directory format to the branch, with the given contents or a default one,
// and commits it with the updated atlas.sum.
func (r *stackRepo) formatFile(branch, format, name string, sql ...string) {
	r.t.Helper()
	r.checkout(branch)
	dir := filepath.Join(r.dev, r.dir)
	require.NoError(r.t, os.MkdirAll(dir, 0755))
	content := fmt.Sprintf("-- %s\nSELECT 1;\n", name)
	if len(sql) > 0 {
		content = sql[0]
	}
	require.NoError(r.t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0755))
	require.NoError(r.t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0644))
	r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir+"?format="+format)
	r.git(r.dev, "add", "--all")
	r.git(r.dev, "commit", "--quiet", "--message", fmt.Sprintf("%s: add %s", branch, name))
}

// hash commits the regenerated atlas.sum of the branch.
func (r *stackRepo) hash(branch string) {
	r.t.Helper()
	r.checkout(branch)
	r.atlas(r.dev, "migrate", "hash", "--dir", "file://"+r.dir)
	r.git(r.dev, "commit", "--quiet", "--all", "--message", fmt.Sprintf("%s: hash", branch))
}

// amend rewrites the last commit of the branch.
func (r *stackRepo) amend(branch, message string) {
	r.t.Helper()
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
	r.t.Helper()
	r.git(r.dev, append([]string{"push", "--quiet", "origin"}, refspecs(branches)...)...)
}

// forcePush force-pushes the branches of the developer clone.
func (r *stackRepo) forcePush(branches ...string) {
	r.t.Helper()
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

// land lands the bottom layer of the stack on its base, with a squash merge or the
// given merge method, and retargets the layer above it, as GitHub does. It keeps the
// head of the merged layer as the head ref of its pull request.
func (r *stackRepo) land(s *atlasaction.Stack, method ...string) {
	r.t.Helper()
	l := &s.Layers[0]
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
	l.Merged, l.Open = true, false
	r.git(r.origin, "update-ref", fmt.Sprintf("refs/pull/%d/head", l.Number), r.tip(l.Branch))
	if len(s.Layers) > 1 {
		s.Layers[1].Base = s.Base
	}
}

// rebase rebases the commits of the branch after upstream onto the given branch, and force-pushes it.
func (r *stackRepo) rebase(branch, onto, upstream string) {
	r.t.Helper()
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.git(r.dev, "checkout", "--quiet", "-B", branch, "origin/"+branch)
	r.git(r.dev, "rebase", "--quiet", "--onto", "origin/"+onto, upstream)
	r.forcePush(branch)
}

// tip returns the head of the branch on the remote.
func (r *stackRepo) tip(branch string) string {
	r.t.Helper()
	return r.gitOut(r.origin, "rev-parse", "refs/heads/"+branch)
}

func (r *stackRepo) tips(branches ...string) []string {
	r.t.Helper()
	tips := make([]string, len(branches))
	for i, b := range branches {
		tips[i] = r.tip(b)
	}
	return tips
}

// log returns the subjects of the commits of the remote branch that are not in the base, oldest first.
func (r *stackRepo) log(base, branch string) []string {
	r.t.Helper()
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
	r.t.Helper()
	var names []string
	for _, f := range r.sum(branch) {
		_, n, _ := strings.Cut(strings.TrimSuffix(f.N, ".sql"), "_")
		names = append(names, n)
	}
	return names
}

// versions returns the versions of the migration files of the remote branch.
func (r *stackRepo) versions(branch string) []string {
	r.t.Helper()
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
	r.t.Helper()
	return r.stackAt(n, 1, base, branches...)
}

// stackAt returns a stack of the given branches, whose pull requests are numbered from first.
func (r *stackRepo) stackAt(n, first int, base string, branches ...string) *atlasaction.Stack {
	r.t.Helper()
	s := &atlasaction.Stack{Number: n, Base: base}
	for i, b := range branches {
		l := atlasaction.StackLayer{Number: first + i, Branch: b, Base: base, Open: true}
		if i > 0 {
			l.Base = branches[i-1]
		}
		// GitHub keeps the head of the pull request as refs/pull/<number>/head.
		r.git(r.origin, "update-ref", fmt.Sprintf("refs/pull/%d/head", l.Number), r.tip(b))
		s.Layers = append(s.Layers, l)
	}
	return s
}

// run runs the action on a push to the branch, in the clone of the action.
func (r *stackRepo) run(s *atlasaction.Stack, o run) (*mockAction, string, error) {
	r.t.Helper()
	scm := &stackSCM{
		origin:     r.origin,
		prs:        o.prs,
		stacks:     o.stacks,
		unreadable: o.unreadable,
		stackErrs:  o.stackErrs,
		unstacked:  o.unstacked,
		pruned:     o.pruned,
	}
	if s != nil {
		scm.stacks = append(scm.stacks, s)
	}
	switch {
	case o.noHistory || o.activity == "none":
		scm.activityErr = fmt.Errorf("%w: activity is not available", github.ErrNotFound)
	case o.activity == "error":
		scm.activityErr = errors.New("API rate limit exceeded")
	}
	scm.partial = o.activity == "partial"
	branch := o.branch
	if !o.keep {
		require.NoError(r.t, os.RemoveAll(r.ci))
		// A local clone copies all the objects of the remote, and not only the ones of its branches.
		r.git(filepath.Dir(r.ci), "clone", "--quiet", "--no-local", r.origin, r.ci)
		r.config(r.ci)
	}
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
	maps.Copy(in, o.inputs)
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
	if len(o.config) > 0 {
		next := run
		run = func(ctx context.Context, name string, args ...string) *exec.Cmd {
			cmd := next(ctx, name, args...)
			if name == "git" {
				env := cmd.Env
				if env == nil {
					env = os.Environ()
				}
				env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(o.config)))
				for i, kv := range o.config {
					k, v, _ := strings.Cut(kv, "=")
					env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, k), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, v))
				}
				cmd.Env = env
			}
			return cmd
		}
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
	err = acts.MigrateAutoRebase(r.t.Context())
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

// BranchHistory implements atlasaction.StackClient. Like the activity of a GitHub repository,
// it reports every head the branch had since it was created, from the reflog of the remote.
func (s *stackSCM) BranchHistory(_ context.Context, branch string) (*atlasaction.BranchHistory, error) {
	s.reads++
	if s.activityErr != nil {
		return nil, s.activityErr
	}
	for _, n := range s.unreadable {
		if s.branchOf(n) == branch {
			return nil, fmt.Errorf("%w: activity of %q is not available", github.ErrNotFound, branch)
		}
	}
	out, err := exec.Command("git", "-C", s.origin, "reflog", "show", "--format=%H", "refs/heads/"+branch).Output()
	if err != nil {
		return nil, err
	}
	h := &atlasaction.BranchHistory{Heads: slices.Compact(strings.Fields(string(out))), Created: !s.partial}
	if s.partial {
		// Only the last change is still recorded.
		h.Heads = h.Heads[:min(2, len(h.Heads))]
	}
	// A change that is not a fast-forward is a force-push.
	for i := 1; i < len(h.Heads) && !h.Rewritten; i++ {
		h.Rewritten = exec.Command("git", "-C", s.origin, "merge-base", "--is-ancestor", h.Heads[i], h.Heads[i-1]).Run() != nil
	}
	if slices.Contains(s.pruned, branch) {
		h.Heads, h.Rewritten = append(h.Heads, prunedHead), true
	}
	return h, nil
}

// ForcePushes implements atlasaction.StackClient. It reports the heads around the changes of the
// branch that are not fast-forwards, from the reflog of the remote.
func (s *stackSCM) ForcePushes(_ context.Context, branch string) ([]string, error) {
	if s.activityErr != nil {
		return nil, s.activityErr
	}
	out, err := exec.Command("git", "-C", s.origin, "reflog", "show", "--format=%H", "refs/heads/"+branch).Output()
	if err != nil {
		return nil, err
	}
	var heads []string
	for all, i := slices.Compact(strings.Fields(string(out))), 1; i < len(all); i++ {
		if exec.Command("git", "-C", s.origin, "merge-base", "--is-ancestor", all[i], all[i-1]).Run() != nil {
			heads = append(heads, all[i-1], all[i])
		}
	}
	return heads, nil
}

// extraHead is a stackSCM whose activity of every branch also lists the given head.
type extraHead struct {
	*stackSCM
	head string
}

// BranchHistory implements atlasaction.StackClient.
func (e *extraHead) BranchHistory(ctx context.Context, branch string) (*atlasaction.BranchHistory, error) {
	h, err := e.stackSCM.BranchHistory(ctx, branch)
	if err == nil {
		h.Heads = append(h.Heads, e.head)
	}
	return h, err
}

// prunedForcePushes is a stackSCM whose force-pushes of a branch include heads the remote no longer has.
type prunedForcePushes struct {
	*stackSCM
	branch string
}

// ForcePushes implements atlasaction.StackClient.
func (p *prunedForcePushes) ForcePushes(ctx context.Context, branch string) ([]string, error) {
	heads, err := p.stackSCM.ForcePushes(ctx, branch)
	if err == nil && branch == p.branch {
		heads = append(heads, prunedHead, prunedHead[:39]+"8")
	}
	return heads, err
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

// prunedHead is a head that the remote no longer has.
const prunedHead = "0123456789abcdef0123456789abcdef01234567"

var _ atlasaction.StackClient = (*stackSCM)(nil)
