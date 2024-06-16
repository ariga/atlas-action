// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/sqlcheck"
	"ariga.io/atlas/sql/sqlclient"
	_ "github.com/mattn/go-sqlite3"
	"github.com/sethvargo/go-githubactions"
	"github.com/stretchr/testify/require"
)

func TestMigrateApply(t *testing.T) {
	t.Run("local dir", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "Migrating to version **20230922132634** (1 migrations in total):")
		require.Contains(t, string(c), "### ✅ Migration Succeeded")
		require.Contains(t, string(c), "- **1 sql statement**")
	})
	t.Run("dry-run", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("dry-run", "true")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
		stat, err := tt.cli.MigrateStatus(context.Background(), &atlasexec.MigrateStatusParams{
			URL:    "sqlite://" + tt.db,
			DirURL: "file://testdata/migrations/",
		})
		require.NoError(t, err)
		require.Empty(t, stat.Applied)
	})
	t.Run("dry-run false", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("dry-run", "false")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
		stat, err := tt.cli.MigrateStatus(context.Background(), &atlasexec.MigrateStatusParams{
			URL:    "sqlite://" + tt.db,
			DirURL: "file://testdata/migrations/",
		})
		require.NoError(t, err)
		require.Len(t, stat.Applied, 1)
	})
	t.Run("tx-mode", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("tx-mode", "fake")
		err := MigrateApply(context.Background(), tt.cli, tt.act)

		// The error here proves that the tx-mode was passed to atlasexec, which
		// is what we want to test.
		exp := `unknown tx-mode "fake"`
		require.ErrorContains(t, err, exp)
		m, err := tt.outputs()
		require.NoError(t, err)
		require.Contains(t, m["error"], exp)
	})
	t.Run("baseline", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("baseline", "111_fake")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		// The error here proves that the baseline was passed to atlasexec, which
		// is what we want to test.
		exp := `Error: baseline version "111_fake" not found`
		require.ErrorContains(t, err, exp)
		m, err := tt.outputs()
		require.NoError(t, err)
		require.EqualValues(t, map[string]string{
			"error": exp,
		}, m)
	})
	t.Run("config-broken", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/broken.hcl")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `"testdata/config/broken.hcl" was not found`)
	})
	t.Run("config", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
	})
}

func TestMigrateDown(t *testing.T) {
	setup := func(t *testing.T) *test {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/down/")
		// Ensure files are applied.
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "Migrating to version **3** (3 migrations in total):")
		require.Contains(t, string(c), "### ✅ Migration Succeeded")
		require.Contains(t, string(c), "- **3 sql statements**")
		tt.resetOut(t)
		tt.setInput("dev-url", "sqlite://dev?mode=memory")
		return tt
	}

	t.Run("down 1 file (default)", func(t *testing.T) {
		tt := setup(t)
		require.NoError(t, MigrateDown(context.Background(), tt.cli, tt.act))
		require.EqualValues(t, map[string]string{
			"current":        "3",
			"target":         "2",
			"planned_count":  "1",
			"reverted_count": "1",
		}, must(tt.outputs()))
	})

	t.Run("down two files", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("amount", "2")
		require.NoError(t, MigrateDown(context.Background(), tt.cli, tt.act))
		require.EqualValues(t, map[string]string{
			"current":        "3",
			"target":         "1",
			"planned_count":  "1", // sqlite has transactional DDL -> one file to apply
			"reverted_count": "1",
		}, must(tt.outputs()))
	})

	t.Run("down to version", func(t *testing.T) {
		t.Run("1", func(t *testing.T) {
			tt := setup(t)
			tt.setInput("to-version", "1")
			require.NoError(t, MigrateDown(context.Background(), tt.cli, tt.act))
			require.EqualValues(t, map[string]string{
				"current":        "3",
				"target":         "1",
				"planned_count":  "1", // sqlite has transactional DDL -> one file to apply
				"reverted_count": "1",
			}, must(tt.outputs()))
		})
		t.Run("2", func(t *testing.T) {
			tt := setup(t)
			tt.setInput("to-version", "2")
			require.NoError(t, MigrateDown(context.Background(), tt.cli, tt.act))
			require.EqualValues(t, map[string]string{
				"current":        "3",
				"target":         "2",
				"planned_count":  "1", // sqlite has transactional DDL -> one file to apply
				"reverted_count": "1",
			}, must(tt.outputs()))
		})
	})

	t.Run("down approval pending", func(t *testing.T) {
		tt := setup(t)
		tt.cli = must(atlasexec.NewClient("", "./mock-atlas-down.sh"))
		tt.setupConfigWithLogin(t, "", "")
		st := must(json.Marshal(atlasexec.MigrateDown{
			URL:    "URL",
			Status: "PENDING_USER",
		}))
		t.Setenv("TEST_STDOUT", string(st))
		tt.setInput("env", "test")
		require.EqualError(t, MigrateDown(context.Background(), tt.cli, tt.act), "plan approval pending, review here: URL")
		require.EqualValues(t, map[string]string{"url": "URL"}, must(tt.outputs()))
	})

	t.Run("aborted", func(t *testing.T) {
		tt := setup(t)
		tt.cli = must(atlasexec.NewClient("", "./mock-atlas-down.sh"))
		tt.setupConfigWithLogin(t, "", "")
		st := must(json.Marshal(atlasexec.MigrateDown{
			URL:    "URL",
			Status: "ABORTED",
		}))
		t.Setenv("TEST_STDOUT", string(st))
		t.Setenv("TEST_EXIT_CODE", "1")
		tt.setInput("env", "test")
		require.EqualError(t, MigrateDown(context.Background(), tt.cli, tt.act), "plan rejected, review here: URL")
		require.EqualValues(t, map[string]string{"url": "URL"}, must(tt.outputs()))
	})

	t.Run("wait configuration", func(t *testing.T) {
		tt := setup(t)
		tt.cli = must(atlasexec.NewClient("", "./mock-atlas-down.sh"))
		tt.setupConfigWithLogin(t, "", "")
		st := must(json.Marshal(atlasexec.MigrateDown{
			URL:    "URL",
			Status: "PENDING_USER",
		}))
		t.Setenv("TEST_STDOUT", string(st))
		tt.setInput("env", "test")
		tt.setInput("wait-interval", "1s") // wait one second before next attempt
		tt.setInput("wait-timeout", "2s")  // stop waiting once one second has passed

		// Considering we are waiting 1 second between attempts (~0 seconds per attempt)
		// and a maximum of 2 second to wait, expect at least 3 retries (1 immediate, 2 retries).
		cf := filepath.Join(t.TempDir(), "counter")
		t.Setenv("TEST_COUNTER_FILE", cf)
		require.EqualError(t, MigrateDown(context.Background(), tt.cli, tt.act), "plan approval pending, review here: URL")
		require.FileExists(t, cf)
		b, err := os.ReadFile(cf)
		require.NoError(t, err)
		i, err := strconv.Atoi(strings.TrimSpace(string(b)))
		require.NoError(t, err)
		require.GreaterOrEqual(t, i, 3)
	})
}

func TestMigratePush(t *testing.T) {
	t.Run("config-broken", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/broken.hcl")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `"testdata/config/broken.hcl" was not found`)
	})
	t.Run("env-broken", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "broken-env")
		tt.setInput("dir-name", "test-dir")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `env "broken-env" not defined in config file`)
	})
	t.Run("broken dir", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("dir", "file://some_broken_dir")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `sql/migrate: stat some_broken_dir: no such file or directory`)
	})
}

func TestMigratePushWithCloud(t *testing.T) {
	token := "123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	t.Run("dev-url broken", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "broken-driver://")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `unknown driver "broken-driver"`)
	})
	t.Run("invalid tag", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "invalid-character@")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `tag must be lowercase alphanumeric`)
	})
	t.Run("tag", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "valid-tag-123")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
	})
	t.Run("config", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("env", "test")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir-name", "test-dir")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
	})
	t.Run("dir-name invalid characters", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-#dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := MigratePush(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "slug must be lowercase alphanumeric")
	})
}

func TestMigrateE2E(t *testing.T) {
	type (
		pushDir struct {
			Slug   string `json:"slug"`
			Tag    string `json:"tag"`
			Driver string `json:"driver"`
			Dir    string `json:"dir"`
		}
		syncDir struct {
			Slug    string                `json:"slug"`
			Driver  string                `json:"driver"`
			Dir     string                `json:"dir"`
			Context *atlasexec.RunContext `json:"context"`
		}
		graphQLQuery struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
			PushDir   *struct {
				pushDir `json:"input"`
			}
			SyncDir *struct {
				syncDir `json:"input"`
			}
		}
	)
	var payloads []graphQLQuery
	token := "123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		query := graphQLQuery{}
		err := json.NewDecoder(r.Body).Decode(&query)
		require.NoError(t, err)
		switch {
		case strings.Contains(query.Query, "diffSyncDir"):
			require.NoError(t, json.Unmarshal(query.Variables, &query.SyncDir))
			payloads = append(payloads, query)
		case strings.Contains(query.Query, "pushDir"):
			require.NoError(t, json.Unmarshal(query.Variables, &query.PushDir))
			payloads = append(payloads, query)
			fmt.Fprint(w, `{"data":{"pushDir":{"url":"https://some-org.atlasgo.cloud/dirs/314159/tags/12345"}}}`)
		}
	}))
	t.Cleanup(srv.Close)
	tt := newT(t)
	tt.setupConfigWithLogin(t, srv.URL, token)
	tt.setInput("dir", "file://testdata/migrations")
	tt.setInput("dir-name", "test-dir")
	tt.setInput("dev-url", "sqlite://file?mode=memory")
	tt.env["GITHUB_REPOSITORY"] = "repository"
	tt.env["GITHUB_HEAD_REF"] = "testing-branch"
	tt.env["GITHUB_REF_NAME"] = "refs/pulls/6/merge"
	tt.env["GITHUB_SHA"] = "sha1234"
	err := os.Setenv("GITHUB_ACTOR", "test-user")
	require.NoError(t, err)
	err = os.Setenv("GITHUB_ACTOR_ID", "123")
	require.NoError(t, err)
	tt.setEvent(t, `{
			"pull_request": {
				"html_url": "http://test"
			}
		}`)
	expected := &atlasexec.RunContext{
		Repo:     "repository",
		Path:     "file://testdata/migrations",
		Branch:   "testing-branch",
		Commit:   "sha1234",
		URL:      "http://test",
		Username: "test-user",
		UserID:   "123",
		SCMType:  "GITHUB",
	}
	err = MigratePush(context.Background(), tt.cli, tt.act)
	require.NoError(t, err)
	require.Equal(t, 2, len(payloads))
	require.Equal(t, "test-dir", payloads[0].SyncDir.Slug)
	require.Equal(t, expected, payloads[0].SyncDir.Context)
	require.Equal(t, payloads[1].PushDir.Tag, "sha1234")
	require.Equal(t, payloads[1].PushDir.Slug, "test-dir")
	tt.env["GITHUB_HEAD_REF"] = ""
	err = MigratePush(context.Background(), tt.cli, tt.act)
	require.Equal(t, 4, len(payloads))
	expected.Branch = tt.env["GITHUB_REF_NAME"]
	require.Equal(t, expected, payloads[2].SyncDir.Context)
	require.NoError(t, err)
	outputs, err := tt.outputs()
	require.NoError(t, err)
	url := outputs["url"]
	require.Equal(t, "https://some-org.atlasgo.cloud/dirs/314159/tags/12345", url)
}

func TestMigrateLint(t *testing.T) {
	type graphQLQuery struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables"`
	}
	type Dir struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Slug    string `json:"slug"`
	}
	type dirsQueryResponse struct {
		Data struct {
			Dirs []Dir `json:"dirs"`
		} `json:"data"`
	}
	token := "123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		var query graphQLQuery
		require.NoError(t, json.NewDecoder(r.Body).Decode(&query))
		switch {
		case strings.Contains(query.Query, "mutation reportMigrationLint"):
			_, _ = fmt.Fprintf(w, `{ "data": { "reportMigrationLint": { "url": "https://migration-lint-report-url" } } }`)
		case strings.Contains(query.Query, "query dirs"):
			dir, err := migrate.NewLocalDir("./testdata/migrations")
			require.NoError(t, err)
			ad, err := migrate.ArchiveDir(dir)
			require.NoError(t, err)
			var resp dirsQueryResponse
			resp.Data.Dirs = []Dir{{
				Name:    "test-dir-name",
				Slug:    "test-dir-slug",
				Content: base64.StdEncoding.EncodeToString(ad),
			}, {
				Name:    "other-dir-name",
				Slug:    "other-dir-slug",
				Content: base64.StdEncoding.EncodeToString(ad),
			}}
			st2bytes, err := json.Marshal(resp)
			require.NoError(t, err)
			_, _ = fmt.Fprint(w, string(st2bytes))
		}
	}))
	t.Run("lint - missing dev-url", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "required flag(s) \"dev-url\" not set")
	})
	t.Run("lint - missing dir", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "stat migrations: no such file or directory")
	})
	t.Run("lint - bad dir name", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "missing required parameter dir-name")
		tt.setInput("dir-name", "fake-dir-name")
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `dir "fake-dir-name" not found`)
		tt.setInput("dir-name", "atlas://test-dir-slug") // user must not add atlas://
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, `slug must be lowercase alphanumeric and may contain /.-_`)
		out, err := tt.outputs()
		require.NoError(t, err)
		require.Equal(t, 0, len(out))
	})
	t.Run("lint summary - lint error", func(t *testing.T) {
		tt := newT(t)
		var comments []pullRequestComment
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			// List comments endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			// Create comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodPost:
				var payload pullRequestComment
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload.ID = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			// Update comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/comments/123" && method == http.MethodPatch:
				require.Len(t, comments, 1)
				comments[0].Body = "updated comment"
				return
			// List pull request files endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/files" && method == http.MethodGet:
				_, err := writer.Write([]byte(`[{"filename": "testdata/migrations_destructive/20230925192914.sql"}]`))
				require.NoError(t, err)
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>testdata/migrations_destructive</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "Error was found")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "error file=testdata/migrations_destructive/20230925192914.sql")
		require.Contains(t, out, "destructive changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#DS102")
		require.Len(t, comments, 1)
		require.Equal(t, "testdata/migrations_destructive/20230925192914.sql", comments[0].Path)
		require.Equal(t, "> [!CAUTION]\n"+
			"> **destructive changes detected**\n"+
			"> Dropping table \"t1\" [DS102](https://atlasgo.io/lint/analyzers#DS102)\n\n"+
			"Add a pre-migration check to ensure table \"t1\" is empty before dropping it\n"+
			"```suggestion\n"+
			"-- atlas:txtar\n"+
			"\n"+
			"-- checks/destructive.sql --"+
			"\n"+
			"-- atlas:assert DS102\n"+
			"SELECT NOT EXISTS (SELECT 1 FROM `t1`) AS `is_empty`;\n"+
			"\n"+
			"-- migration.sql --\n"+
			"drop table t1;\n"+
			"```\n"+
			"Ensure to run `atlas migrate hash --dir \"file://testdata/migrations_destructive\"` after applying the suggested changes.\n"+
			"<!-- generated by ariga/atlas-action for Add a pre-migration check to ensure table \"t1\" is empty before dropping it -->", comments[0].Body)
		require.Equal(t, 1, comments[0].Line)
		// Run Lint against a directory that has an existing suggestion comment, expecting a PATCH of the comment
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Len(t, comments, 1)
		require.Equal(t, "updated comment", comments[0].Body)
	})
	t.Run("lint summary - lint error - working directory is set", func(t *testing.T) {
		tt := newT(t)
		// Same as the previous test but with working directory input set.
		require.NoError(t, os.Chdir("testdata"))
		t.Cleanup(func() {
			err := os.Chdir("..")
			require.NoError(t, err)
		})
		tt.setInput("working-directory", "testdata")
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
			var query graphQLQuery
			require.NoError(t, json.NewDecoder(r.Body).Decode(&query))
			switch {
			case strings.Contains(query.Query, "mutation reportMigrationLint"):
				_, _ = fmt.Fprintf(w, `{ "data": { "reportMigrationLint": { "url": "https://migration-lint-report-url" } } }`)
			case strings.Contains(query.Query, "query dirs"):
				dir, err := migrate.NewLocalDir("./migrations")
				require.NoError(t, err)
				ad, err := migrate.ArchiveDir(dir)
				require.NoError(t, err)
				var resp dirsQueryResponse
				resp.Data.Dirs = []Dir{{
					Name:    "test-dir-name",
					Slug:    "test-dir-slug",
					Content: base64.StdEncoding.EncodeToString(ad),
				}, {
					Name:    "other-dir-name",
					Slug:    "other-dir-slug",
					Content: base64.StdEncoding.EncodeToString(ad),
				}}
				st2bytes, err := json.Marshal(resp)
				require.NoError(t, err)
				_, _ = fmt.Fprint(w, string(st2bytes))
			}
		}))

		var comments []pullRequestComment
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			// List comments endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			// Create comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodPost:
				var payload pullRequestComment
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload.ID = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			// Update comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/comments/123" && method == http.MethodPatch:
				require.Len(t, comments, 1)
				comments[0].Body = "updated comment"
				return
			// List pull request files endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/files" && method == http.MethodGet:
				_, err := writer.Write([]byte(`[{"filename": "testdata/migrations_destructive/20230925192914.sql"}]`))
				require.NoError(t, err)
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>migrations_destructive</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "Error was found")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "error file=testdata/migrations_destructive/20230925192914.sql")
		require.Contains(t, out, "destructive changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#DS102")
		require.Len(t, comments, 1)
		require.Equal(t, "testdata/migrations_destructive/20230925192914.sql", comments[0].Path)
		require.Equal(t, "> [!CAUTION]\n"+
			"> **destructive changes detected**\n"+
			"> Dropping table \"t1\" [DS102](https://atlasgo.io/lint/analyzers#DS102)\n\n"+
			"Add a pre-migration check to ensure table \"t1\" is empty before dropping it\n"+
			"```suggestion\n"+
			"-- atlas:txtar\n"+
			"\n"+
			"-- checks/destructive.sql --"+
			"\n"+
			"-- atlas:assert DS102\n"+
			"SELECT NOT EXISTS (SELECT 1 FROM `t1`) AS `is_empty`;\n"+
			"\n"+
			"-- migration.sql --\n"+
			"drop table t1;\n"+
			"```\n"+
			"Ensure to run `atlas migrate hash --dir \"file://migrations_destructive\"` after applying the suggested changes.\n"+
			"<!-- generated by ariga/atlas-action for Add a pre-migration check to ensure table \"t1\" is empty before dropping it -->", comments[0].Body)
		require.Equal(t, 1, comments[0].Line)
		// Run Lint against a directory that has an existing suggestion comment, expecting a PATCH of the comment
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Len(t, comments, 1)
		require.Equal(t, "updated comment", comments[0].Body)
	})
	t.Run("lint summary - lint error - push event", func(t *testing.T) {
		tt := newT(t)
		tt.env["GITHUB_EVENT_NAME"] = "push"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "`atlas migrate lint` completed with errors, see report: https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		// Check there is no summary file created in case of non-pull request event
		require.Empty(t, string(c))
		require.Empty(t, tt.out.String())
	})
	t.Run("lint summary - with diagnostics file not included in the pull request", func(t *testing.T) {
		tt := newT(t)
		var comments []pullRequestComment
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			// List comments endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			// Create comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodPost:
				var payload pullRequestComment
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload.ID = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			// List pull request files endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/files" && method == http.MethodGet:
				_, err := writer.Write([]byte(`[{"filename": "new_file.sql"}]`))
				require.NoError(t, err)
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/diagnostics")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>testdata/diagnostics</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 issue was found")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "warning file=testdata/diagnostics/20231016114135_add_not_null.sql")
		require.Contains(t, out, "data dependent changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#MF103")
		// Assert no comments were created, since migration file is not included in the pull request
		require.Len(t, comments, 0)
	})
	t.Run("lint summary - lint success", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>testdata/migrations</strong>\n")
		require.Contains(t, sum, "1 new migration file detected")
		require.Contains(t, sum, "No issues found")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
	})
	t.Run("lint summary - lint success - vars input", func(t *testing.T) {
		tt := newT(t)
		cfgURL := generateHCLWithVars(t)
		tt.setInput("config", cfgURL)
		tt.setInput("vars", fmt.Sprintf(`{"token":"%s", "url":"%s"}`, token, srv.URL))
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
	})
	t.Run("lint comment", func(t *testing.T) {
		tt := newT(t)
		type ghPayload struct {
			Body   string
			URL    string
			Method string
		}
		var ghPayloads []ghPayload
		commentRegex := regexp.MustCompile("<!-- generated by ariga/atlas-action for [a-zA-Z-]* -->")
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var payload struct {
				Body string `json:"body"`
			}
			if request.Method != http.MethodGet {
				err := json.NewDecoder(request.Body).Decode(&payload)
				require.NoError(t, err)
			}
			ghPayloads = append(ghPayloads, ghPayload{
				Body:   payload.Body,
				URL:    request.URL.Path,
				Method: request.Method,
			})
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			// List issues comments endpoint
			case path == "/repos/test-owner/test-repository/issues/42/comments" && method == http.MethodGet:
				comments := `[
            					{"id": 123, "body": "first awesome comment"},
            					{"id": 456, "body": "may the force be with you"},
            					{"id": 789, "body": "<!-- generated by ariga/atlas-action for other-dir-slug -->"}
            				]`
				_, err := writer.Write([]byte(comments))
				require.NoError(t, err)
			// Create issue comment endpoint
			case path == "/repos/test-owner/test-repository/issues/42/comments" && method == http.MethodPost:
				require.Regexp(t, commentRegex, payload.Body)
				writer.WriteHeader(http.StatusCreated)
			// Update issue comment endpoint
			case path == "/repos/test-owner/test-repository/issues/comments/789":
				require.Regexp(t, commentRegex, payload.Body)
			// List pull request comments endpoint
			case path == "/repos/test-owner/test-repository/pulls/42/comments" && method == http.MethodGet:
				_, err := writer.Write([]byte(`[]`))
				require.NoError(t, err)
			// Create pull request comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/42/comments" && method == http.MethodPost:
				writer.WriteHeader(http.StatusCreated)
			// List pull request files endpoint
			case path == "/repos/test-owner/test-repository/pulls/42/files" && method == http.MethodGet:
				_, err := writer.Write([]byte(`[{"filename": "testdata/migrations_destructive/20230925192914.sql"}]`))
				require.NoError(t, err)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setEvent(t, `{
			"pull_request": {
				"number": 42
			}
		}`)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.env["GITHUB_TOKEN"] = "very-secret-gh-token"
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		// Run Lint while expecting no errors
		err := MigrateLint(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)
		require.Equal(t, 2, len(ghPayloads))
		found := slices.IndexFunc(ghPayloads, func(gh ghPayload) bool {
			if gh.Method != http.MethodPost {
				return false
			}
			if !strings.Contains(gh.Body, "No issues found") {
				return false
			}
			return strings.Contains(gh.Body, "generated by ariga/atlas-action for test-dir-slug")
		})
		require.NotEqual(t, -1, found)
		// Run Lint but this time with lint errors expected
		tt.setInput("dir", "file://testdata/migrations_destructive")
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Equal(t, 7, len(ghPayloads))
		found = slices.IndexFunc(ghPayloads, func(gh ghPayload) bool {
			if gh.Method != http.MethodPost {
				return false
			}
			if !strings.Contains(gh.Body, "Error was found") {
				return false
			}
			return strings.Contains(gh.Body, "generated by ariga/atlas-action for test-dir-slug")
		})
		require.NotEqual(t, -1, found)
		// Run Lint against a directory that has an existing comment, expecting a PATCH
		tt.setInput("dir-name", "other-dir-slug")
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Equal(t, 12, len(ghPayloads))
		found = slices.IndexFunc(ghPayloads, func(gh ghPayload) bool {
			if gh.Method != http.MethodPatch {
				return false
			}
			if !strings.Contains(gh.Body, "Error was found") {
				return false
			}
			return strings.Contains(gh.Body, "generated by ariga/atlas-action for other-dir-slug")
		})
		require.NotEqual(t, -1, found)
		// Run Lint with input errors, no calls to github api should be made
		tt.setInput("dir-name", "fake-dir-name")
		err = MigrateLint(context.Background(), tt.cli, tt.act)
		require.Equal(t, 12, len(ghPayloads))
		require.ErrorContains(t, err, `dir "fake-dir-name" not found`)
	})
}

func TestLintTemplateGeneration(t *testing.T) {
	type env struct {
		Driver string         `json:"Driver,omitempty"`
		URL    *sqlclient.URL `json:"URL,omitempty"`
		Dir    string         `json:"Dir,omitempty"`
	}
	for _, tt := range []struct {
		name     string
		payload  *atlasexec.SummaryReport
		expected string // expected HTML output of the comment template
	}{
		{
			name: "no errors",
			payload: &atlasexec.SummaryReport{
				URL: "https://migration-lint-report-url",
				Env: env{
					Dir: "testdata/migrations",
				},
				Files: []*atlasexec.FileReport{{}},
			},
			// language=html
			expected: `<table>
    <thead>
        <tr>
            <th>Status</th>
            <th>Step</th>
            <th>Result</th>
        </tr>
    </thead>
    <tbody>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                1 new migration file detected
            </td>
            <td>&nbsp;</td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                ERD and visual diff generated
            </td>
            <td>
                <a href="https://migration-lint-report-url#erd" target="_blank">View Visualization</a>
            </td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                No issues found
            </td>
            <td>
                <a href="https://migration-lint-report-url" target="_blank">View Report</a>
            </td>
        </tr>
    <td colspan="4">
        <div align="center">
            Read the full linting report on <a href="https://migration-lint-report-url" target="_blank">Atlas Cloud</a>
        </div>
    </td>
    </tbody>
</table>`,
		},
		{
			name: "file with 2 issues",
			payload: &atlasexec.SummaryReport{
				URL: "https://migration-lint-report-url",
				Env: env{
					Dir: "testdata/migrations",
				},
				Files: []*atlasexec.FileReport{{
					Name: "20230925192914.sql",
					Reports: []sqlcheck.Report{
						{
							Diagnostics: []sqlcheck.Diagnostic{
								{
									Text: "Add unique index to existing column",
									Code: "MF101",
								},
								{
									Text: "Adding a non-nullable column to a table without a DEFAULT",
									Code: "MY101",
								},
							},
						},
					},
				}},
			},
			// language=html
			expected: `<table>
    <thead>
        <tr>
            <th>Status</th>
            <th>Step</th>
            <th>Result</th>
        </tr>
    </thead>
    <tbody>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                1 new migration file detected
            </td>
            <td>&nbsp;</td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                ERD and visual diff generated
            </td>
            <td>
                <a href="https://migration-lint-report-url#erd" target="_blank">View Visualization</a>
            </td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/warning.svg"/>
                </div>
            </td>
            <td>
                Analyze 20230925192914.sql
                <br/>2 issues were found
            </td>
            <td>
                Add unique index to existing column <a href="https://atlasgo.io/lint/analyzers#MF101" target="_blank">(MF101)</a> <br/>
                Adding a non-nullable column to a table without a DEFAULT <a href="https://atlasgo.io/lint/analyzers#MY101" target="_blank">(MY101)</a> <br/>
            </td>
        </tr>
    <td colspan="4">
        <div align="center">
            Read the full linting report on <a href="https://migration-lint-report-url" target="_blank">Atlas Cloud</a>
        </div>
    </td>
    </tbody>
</table>`,
		},
		{
			name: "2 files, 1 with error, 1 with issue",
			payload: &atlasexec.SummaryReport{
				URL: "https://migration-lint-report-url",
				Env: env{
					Dir: "testdata/migrations",
				},
				Files: []*atlasexec.FileReport{{
					Name:  "20230925192914.sql",
					Error: "Destructive changes detected",
				},
					{
						Name: "20230925192915.sql",
						Reports: []sqlcheck.Report{
							{
								Diagnostics: []sqlcheck.Diagnostic{
									{
										Text: "Missing the CONCURRENTLY in index creation",
										Code: "PG101",
									},
								},
							},
						},
					},
				},
			},
			// language=html
			expected: `<table>
    <thead>
        <tr>
            <th>Status</th>
            <th>Step</th>
            <th>Result</th>
        </tr>
    </thead>
    <tbody>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                2 new migration files detected
            </td>
            <td>&nbsp;</td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                ERD and visual diff generated
            </td>
            <td>
                <a href="https://migration-lint-report-url#erd" target="_blank">View Visualization</a>
            </td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/error.svg"/>
                </div>
            </td>
            <td>
                Analyze 20230925192914.sql
                <br/> Error was found
            </td>
            <td>
                Destructive changes detected
            </td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/warning.svg"/>
                </div>
            </td>
            <td>
                Analyze 20230925192915.sql
                <br/>1 issue was found
            </td>
            <td>
                Missing the CONCURRENTLY in index creation <a href="https://atlasgo.io/lint/analyzers#PG101" target="_blank">(PG101)</a> <br/>
            </td>
        </tr>
    <td colspan="4">
        <div align="center">
            Read the full linting report on <a href="https://migration-lint-report-url" target="_blank">Atlas Cloud</a>
        </div>
    </td>
    </tbody>
</table>`,
		},
		{
			name: "1 checksum error",
			payload: &atlasexec.SummaryReport{
				URL: "https://migration-lint-report-url",
				Env: env{
					Dir: "testdata/migrations",
				},
				Files: []*atlasexec.FileReport{{
					Name:  "20230925192914.sql",
					Error: "checksum mismatch",
				}},
			},
			// language=html
			expected: `<table>
    <thead>
        <tr>
            <th>Status</th>
            <th>Step</th>
            <th>Result</th>
        </tr>
    </thead>
    <tbody>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                1 new migration file detected
            </td>
            <td>&nbsp;</td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/success.svg"/>
                </div>
            </td>
            <td>
                ERD and visual diff generated
            </td>
            <td>
                <a href="https://migration-lint-report-url#erd" target="_blank">View Visualization</a>
            </td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/error.svg"/>
                </div>
            </td>
            <td>
                Analyze 20230925192914.sql
                <br/> Error was found
            </td>
            <td>
                checksum mismatch
            </td>
        </tr>
    <td colspan="4">
        <div align="center">
            Read the full linting report on <a href="https://migration-lint-report-url" target="_blank">Atlas Cloud</a>
        </div>
    </td>
    </tbody>
</table>`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, lintComment.Execute(&buf, tt.payload))
			require.Contains(t, buf.String(), tt.expected)
		})
	}
}

func TestApplyTemplateGeneration(t *testing.T) {
	for _, tt := range []struct {
		name     string
		payload  *atlasexec.MigrateApply
		expected string // expected output of the comment template
	}{
		{
			name: "no errors 2 files, 3 migrations",
			payload: &atlasexec.MigrateApply{
				Env: atlasexec.Env{
					Driver: "sqlite",
					Dir:    "testdata/migrations",
					URL: &sqlclient.URL{
						URL: &url.URL{
							Scheme:   "sqlite",
							Host:     "file",
							RawQuery: "_fk=1&mode=memory",
						},
						Schema: "main",
					},
				},
				Pending: []atlasexec.File{
					{
						Name:    "20221108173626.sql",
						Version: "20221108173626",
					},
					{
						Name:    "20221108173658.sql",
						Version: "20221108173658",
					},
				},
				Applied: []*atlasexec.AppliedFile{
					{
						File: atlasexec.File{
							Name:    "20221108173626.sql",
							Version: "20221108173626",
						},
						Start: must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.914578+03:00")),
						End:   must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.940343+03:00")),
						Applied: []string{
							"CREATE TABLE `dept_emp_latest_date` (`emp_no` int NOT NULL, `from_date` date NULL, `to_date` date NULL) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci COMMENT \"VIEW\";",
							"CREATE TABLE `employees` (`emp_no` int NOT NULL, `birth_date` date NOT NULL, `first_name` varchar(14) NOT NULL, `last_name` varchar(16) NOT NULL, `gender` enum('M','F') NOT NULL, `hire_date` date NOT NULL, PRIMARY KEY (`emp_no`)) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;",
						},
					},
					{
						File: atlasexec.File{
							Name:    "20221108173658.sql",
							Version: "20221108173658",
						},
						Start: must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.940343+03:00")),
						End:   must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.963743+03:00")),
						Applied: []string{
							"CREATE TABLE `employees` (`emp_no` int NOT NULL, `birth_date` date NOT NULL, `first_name` varchar(14) NOT NULL, `last_name` varchar(16) NOT NULL, `gender` enum('M','F') NOT NULL, `hire_date` date NOT NULL, PRIMARY KEY (`emp_no`)) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;",
						},
					},
				},
				Current: "20221108143624",
				Target:  "20221108173658",
				Start:   must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.909446+03:00")),
				End:     must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.963743+03:00")),
			},
			// language=markdown
			expected: "Running `atlas migrate apply` with **testdata/migrations** Directory, on `sqlite://file?_fk=1&mode=memory`\n\n### Migration Summary\nMigrating to version **20221108173658** from **20221108143624** (2 migrations in total):\n### ✅ Migration Succeeded\n\n\n- **54.297ms**\n- **2 migrations**\n- **3 sql statements**\n\n### Applied Migrations\n\n<table>\n    <tr>\n        <th>Status</th>\n        <th>File Name</th>\n        <th>Executed Statements</th>\n        <th>Execution Time</th>\n    </tr>\n    <tr>\n        <td>✅ Succeeded</td>\n        <td>20221108173626.sql</td>\n        <td>2</td>\n        <td>25.765ms</td>\n    </tr>\n    <tr>\n        <td>✅ Succeeded</td>\n        <td>20221108173658.sql</td>\n        <td>1</td>\n        <td>23.4ms</td>\n    </tr>\n</table>\n\n<details>\n\n<summary><h3>SQL Statements</h3></summary>\n\n```sql\n\n-- File: 20221108173626.sql\nCREATE TABLE `dept_emp_latest_date` (`emp_no` int NOT NULL, `from_date` date NULL, `to_date` date NULL) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci COMMENT \"VIEW\";\nCREATE TABLE `employees` (`emp_no` int NOT NULL, `birth_date` date NOT NULL, `first_name` varchar(14) NOT NULL, `last_name` varchar(16) NOT NULL, `gender` enum('M','F') NOT NULL, `hire_date` date NOT NULL, PRIMARY KEY (`emp_no`)) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n\n-- File: 20221108173658.sql\nCREATE TABLE `employees` (`emp_no` int NOT NULL, `birth_date` date NOT NULL, `first_name` varchar(14) NOT NULL, `last_name` varchar(16) NOT NULL, `gender` enum('M','F') NOT NULL, `hire_date` date NOT NULL, PRIMARY KEY (`emp_no`)) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n```\n\n</details>",
		},
		{
			name: "2 files, 1 with error",
			payload: &atlasexec.MigrateApply{
				Env: atlasexec.Env{
					Driver: "mysql",
					Dir:    "testdata/migrations",
					URL: &sqlclient.URL{
						URL: &url.URL{
							Scheme:   "mysql",
							Host:     "localhost:3306",
							Path:     "/test",
							RawQuery: "parseTime=true",
						},
						Schema: "test",
					},
				},
				Pending: []atlasexec.File{
					{
						Name:    "20221108173626.sql",
						Version: "20221108173626",
					},
					{
						Name:    "20221108173658.sql",
						Version: "20221108173658",
					},
				},
				Applied: []*atlasexec.AppliedFile{
					{
						File: atlasexec.File{
							Name:    "20221108173626.sql",
							Version: "20221108173626",
						},
						Start: must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.914578+03:00")),
						End:   must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.940343+03:00")),
						Applied: []string{
							"CREATE TABLE Persons ( PersonID int );",
						},
					},
					{
						File: atlasexec.File{
							Name:    "20221108173658.sql",
							Version: "20221108173658",
						},
						Start: must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.940343+03:00")),
						End:   must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.963743+03:00")),
						Applied: []string{
							"create Table Err?",
						},
						Error: &struct {
							SQL   string
							Error string
						}{
							SQL:   "create Table Err?",
							Error: "Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near '?' at line 1",
						},
					},
				},
				Current: "20221108143624",
				Target:  "20221108173658",
				Start:   must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.909446+03:00")),
				End:     must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.963743+03:00")),
				Error:   "sql/migrate: executing statement \"create Table Err?\" from version \"20240616125213\": Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near '?' at line 1",
			},
			// language=markdown
			expected: "Running `atlas migrate apply` with **testdata/migrations** Directory, on `mysql://localhost:3306/test?parseTime=true`\n\n### Migration Summary\nMigrating to version **20221108173658** from **20221108143624** (2 migrations in total):\n### ❌ Migration Failed\n- **Error:** sql/migrate: executing statement \"create Table Err?\" from version \"20240616125213\": Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near '?' at line 1\n\n\n- **54.297ms**\n- **1 migration ok, 1 with errors**\n- **1 sql statement ok, 1 with errors**\n\n### Applied Migrations\n\n<table>\n    <tr>\n        <th>Status</th>\n        <th>File Name</th>\n        <th>Executed Statements</th>\n        <th>Execution Time</th>\n    </tr>\n    <tr>\n        <td>✅ Succeeded</td>\n        <td>20221108173626.sql</td>\n        <td>1</td>\n        <td>25.765ms</td>\n    </tr>\n    <tr>\n        <td>❌ Failed</td>\n        <td>20221108173658.sql</td>\n        <td>1</td>\n        <td>23.4ms</td>\n    </tr>\n</table>\n\n<details>\n\n<summary><h3>SQL Statements</h3></summary>\n\n```sql\n\n-- File: 20221108173626.sql\nCREATE TABLE Persons ( PersonID int );\n\n-- File: 20221108173658.sql\n-- Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near '?' at line 1\ncreate Table Err?\n```\n\n</details>",
		},
		{
			name: "no work migration",
			payload: &atlasexec.MigrateApply{
				Env: atlasexec.Env{
					Driver: "mysql",
					Dir:    "testdata/migrations",
					URL: &sqlclient.URL{
						URL: &url.URL{
							Scheme:   "mysql",
							Host:     "localhost:3306",
							Path:     "/test",
							RawQuery: "parseTime=true",
						},
						Schema: "test",
					},
				},
				Current: "20240616130838",
				Start:   must(time.Parse(time.RFC3339, "2024-06-16T16:09:01.683771+03:00")),
				End:     must(time.Parse(time.RFC3339, "2024-06-16T16:09:01.683771+03:00")),
			},
			expected: "### Migration Summary\n- **No migration files to execute.**",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, applyComment.Execute(&buf, tt.payload))
			require.Contains(t, buf.String(), tt.expected)
		})
	}
}

func generateHCL(t *testing.T, url, token string) string {
	st := fmt.Sprintf(
		`atlas { 
			cloud {	
				token = %q
				url = %q
			}
		}
		env "test" {}
		`, token, url)
	atlasConfigURL, clean, err := atlasexec.TempFile(st, "hcl")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, clean())
	})
	return atlasConfigURL
}

func generateHCLWithVars(t *testing.T) string {
	hcl := `
variable "token" {
  type = string
}

variable "url" {
  type = string
}
atlas {
  cloud {
    token = var.token
    url   = var.url
  }
}
env "test" {}
`
	atlasConfigURL, clean, err := atlasexec.TempFile(hcl, "hcl")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, clean())
	})
	return atlasConfigURL
}

func (tt *test) setupConfigWithLogin(t *testing.T, url, token string) {
	tt.setInput("config", generateHCL(t, url, token))
}

func TestMigrateApplyCloud(t *testing.T) {
	handler := func(payloads *[]string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
			body := readBody(t, r.Body)
			*payloads = append(*payloads, body)
			switch b := body; {
			case strings.Contains(b, "query dirState"):
				dir := testDir(t, "./testdata/migrations")
				ad, err := migrate.ArchiveDir(&dir)
				require.NoError(t, err)
				fmt.Fprintf(w, `{"data":{"dirState":{"content":%q}}}`, base64.StdEncoding.EncodeToString(ad))
			case strings.Contains(b, "mutation ReportMigration"):
				fmt.Fprintf(w, `{"data":{"reportMigration":{"url":"https://atlas.com"}}}`)
			case strings.Contains(b, "query Bot"):
			default:
				t.Log("Unhandled call: ", body)
			}
		}
	}
	t.Run("basic", func(t *testing.T) {
		Version = "v1.2.3"
		var payloads []string
		srv := httptest.NewServer(handler(&payloads))
		t.Cleanup(srv.Close)

		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "atlas://cloud-project")
		tt.setInput("env", "test")

		// This isn't simulating a user input but is a workaround for testing Cloud API calls.
		cfgURL := generateHCL(t, srv.URL, "token")
		tt.setInput("config", cfgURL)
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)

		require.Len(t, payloads, 3)
		require.Contains(t, payloads[0], "query Bot")
		require.Contains(t, payloads[1], "query dirState")
		require.Contains(t, payloads[2], "mutation ReportMigration")
		require.Contains(t, payloads[2], `"context":{"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"}`)

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "Migrating to version **20230922132634** (1 migrations in total):")
		require.Contains(t, string(c), "### ✅ Migration Succeeded")
		require.Contains(t, string(c), "- **1 sql statement**")
	})
	t.Run("no-env", func(t *testing.T) {
		var payloads []string
		srv := httptest.NewServer(handler(&payloads))
		t.Cleanup(srv.Close)

		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "atlas://cloud-project")

		// This isn't simulating a user input but is a workaround for testing Cloud API calls.
		cfgURL := generateHCL(t, srv.URL, "token")
		tt.setInput("config", cfgURL)

		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)

		require.Len(t, payloads, 2)
		require.Contains(t, payloads[0], "query Bot")
		require.Contains(t, payloads[1], "query dirState")

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "Migrating to version **20230922132634** (1 migrations in total):")
		require.Contains(t, string(c), "### ✅ Migration Succeeded")
		require.Contains(t, string(c), "- **1 sql statement**")
	})
}

func readBody(t *testing.T, r io.Reader) string {
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}

// sqlitedb returns a path to an initialized sqlite database file. The file is
// created in a temporary directory and will be deleted when the test finishes.
func sqlitedb(t *testing.T) string {
	td := t.TempDir()
	dbpath := filepath.Join(td, "file.db")
	_, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?cache=shared&_fk=1", dbpath))
	require.NoError(t, err)
	return dbpath
}

type test struct {
	db  string
	env map[string]string
	out bytes.Buffer
	cli *atlasexec.Client
	act Action
}

func newT(t *testing.T) *test {
	outputFile, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer outputFile.Close()
	summaryFile, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer summaryFile.Close()
	eventPath, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer eventPath.Close()
	tt := &test{
		db: sqlitedb(t),
		env: map[string]string{
			"GITHUB_OUTPUT":       outputFile.Name(),
			"GITHUB_STEP_SUMMARY": summaryFile.Name(),
			"GITHUB_EVENT_PATH":   eventPath.Name(),
			"GITHUB_EVENT_NAME":   "pull_request",
		},
	}
	tt.setEvent(t, `{}`)
	tt.act = NewGHAction(
		githubactions.WithGetenv(func(key string) string {
			return tt.env[key]
		}),
		githubactions.WithWriter(&tt.out),
	)
	cli, err := atlasexec.NewClient("", "atlas")
	require.NoError(t, err)
	tt.cli = cli
	return tt
}

func (tt *test) setInput(k, v string) {
	tt.env["INPUT_"+strings.ToUpper(k)] = v
}

func (tt *test) setEvent(test *testing.T, payload string) {
	err := os.WriteFile(tt.env["GITHUB_EVENT_PATH"], []byte(payload), 0644)
	require.NoError(test, err)
}

// outputs is a helper that parses the GitHub Actions output file format. This is
// used to parse the output file written by the action.
func (tt *test) outputs() (map[string]string, error) {
	var (
		key   string
		value strings.Builder
		token = "_GitHubActionsFileCommandDelimeter_"
	)
	m := make(map[string]string)
	c, err := os.ReadFile(tt.env["GITHUB_OUTPUT"])
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(c), "\n")
	for _, line := range lines {
		if delim := "<<" + token; strings.Contains(line, delim) {
			key = strings.TrimSpace(strings.Split(line, delim)[0])
			continue
		}
		if strings.Contains(line, token) {
			m[key] = strings.TrimSpace(value.String())
			value.Reset()
			continue
		}
		value.WriteString(line)
	}
	return m, nil
}

func (tt *test) resetOut(t *testing.T) {
	f, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer f.Close()
	tt.env["GITHUB_OUTPUT"] = f.Name()
}

func TestParseGitHubOutputFile(t *testing.T) {
	tt := newT(t)
	tt.act.SetOutput("foo", "bar")
	tt.act.SetOutput("baz", "qux")
	out, err := tt.outputs()
	require.NoError(t, err)
	require.EqualValues(t, map[string]string{
		"foo": "bar",
		"baz": "qux",
	}, out)
}

func TestSetInput(t *testing.T) {
	tt := newT(t)
	tt.setInput("hello-world", "greetings")
	tt.setInput("goodbye-friends", "farewell")

	require.Equal(t, "greetings", tt.act.GetInput("hello-world"))
	require.Equal(t, "farewell", tt.act.GetInput("goodbye-friends"))
}

// testDir returns a migrate.MemDir from the given path.
func testDir(t *testing.T, path string) (d migrate.MemDir) {
	rd, err := os.ReadDir(path)
	require.NoError(t, err)
	for _, f := range rd {
		fp := filepath.Join(path, f.Name())
		b, err := os.ReadFile(fp)
		require.NoError(t, err)
		require.NoError(t, d.WriteFile(f.Name(), b))
	}
	return d
}

func must[T any](t T, err error) T {
	if err != nil {
		panic(err)
	}
	return t
}
