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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/sqlcheck"
	"ariga.io/atlas/sql/sqlclient"
	_ "github.com/mattn/go-sqlite3"
	"github.com/sethvargo/go-githubactions"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestMigrateApply(t *testing.T) {
	t.Run("local dir", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
		require.NoError(t, err)

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n       <td>\n        <code>20230922132634</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "1 migration file, 1 statement passed")
	})
	t.Run("broken migration dir", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/broken/")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
		require.EqualError(t, err, "sql/migrate: executing statement \"CREATE TABLE OrderDetails (\\n    OrderDetailID INTEGER PRIMARY KEY AUTOINCREMENT,\\n    OrderID INTEGER-\\n);\" from version \"20240619073319\": near \"-\": syntax error")

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n       <td>\n        <code>20240619073319</code>")
		require.Contains(t, string(c), "Migration Failed")
		require.Contains(t, string(c), "2 migration files, 3 statements passed, 1 failed")

	})
	t.Run("dry-run", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("dry-run", "true")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())

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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
		require.ErrorContains(t, err, `"testdata/config/broken.hcl" was not found`)
	})
	t.Run("config", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
		require.NoError(t, err)
	})
}

func TestMigrateDown(t *testing.T) {
	setup := func(t *testing.T) *test {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/down/")
		// Ensure files are applied.
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n       <td>\n        <code>3</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "3 migration files, 3 statements passed")
		tt.resetOut(t)
		tt.setInput("dev-url", "sqlite://dev?mode=memory")
		return tt
	}

	t.Run("down 1 file (default)", func(t *testing.T) {
		tt := setup(t)
		require.NoError(t, (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateDown(context.Background()))
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
		require.NoError(t, (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateDown(context.Background()))
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
			require.NoError(t, (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateDown(context.Background()))
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
			require.NoError(t, (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateDown(context.Background()))
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
		require.EqualError(t, (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateDown(context.Background()), "plan approval pending, review here: URL")
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
		require.EqualError(t, (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateDown(context.Background()), "plan rejected, review here: URL")
		require.EqualValues(t, map[string]string{"url": "URL"}, must(tt.outputs()))
	})

	t.Run("wait configuration", func(t *testing.T) {
		tt := setup(t)
		tt.setupConfigWithLogin(t, "", "")
		tt.setInput("env", "test")
		tt.setInput("wait-interval", "1s") // wait one second before next attempt
		tt.setInput("wait-timeout", "2s")  // stop waiting once one second has passed
		// Considering we are waiting 1 second between attempts (~0 seconds per attempt)
		// and a maximum of 2 second to wait, expect at least 3 retries (1 immediate, 2 retries).
		counter := 0
		actions := &Actions{
			Action: tt.act,
			Atlas: &mockAtlas{
				migrateDown: func(ctx context.Context, params *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error) {
					counter++
					return &atlasexec.MigrateDown{
						URL:    "URL",
						Status: "PENDING_USER",
					}, nil
				},
			},
		}
		require.EqualError(t, actions.MigrateDown(context.Background()), "plan approval pending, review here: URL")
		require.GreaterOrEqual(t, counter, 3)
	})
}

type mockAtlas struct {
	migrateDown       func(context.Context, *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error)
	schemaPlan        func(context.Context, *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error)
	schemaPlanLint    func(context.Context, *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error)
	schemaPlanPull    func(context.Context, *atlasexec.SchemaPlanPullParams) (string, error)
	schemaPlanApprove func(context.Context, *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error)
}

var _ AtlasExec = (*mockAtlas)(nil)

// MigrateStatus implements AtlasExec.
func (m *mockAtlas) MigrateStatus(context.Context, *atlasexec.MigrateStatusParams) (*atlasexec.MigrateStatus, error) {
	panic("unimplemented")
}

// MigrateApplySlice implements AtlasExec.
func (m *mockAtlas) MigrateApplySlice(context.Context, *atlasexec.MigrateApplyParams) ([]*atlasexec.MigrateApply, error) {
	panic("unimplemented")
}

// MigrateLintError implements AtlasExec.
func (m *mockAtlas) MigrateLintError(context.Context, *atlasexec.MigrateLintParams) error {
	panic("unimplemented")
}

// MigratePush implements AtlasExec.
func (m *mockAtlas) MigratePush(context.Context, *atlasexec.MigratePushParams) (string, error) {
	panic("unimplemented")
}

// MigrateTest implements AtlasExec.
func (m *mockAtlas) MigrateTest(context.Context, *atlasexec.MigrateTestParams) (string, error) {
	panic("unimplemented")
}

// SchemaTest implements AtlasExec.
func (m *mockAtlas) SchemaTest(context.Context, *atlasexec.SchemaTestParams) (string, error) {
	panic("unimplemented")
}

// SchemaPlan implements AtlasExec.
func (m *mockAtlas) SchemaPlan(ctx context.Context, p *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error) {
	return m.schemaPlan(ctx, p)
}

// SchemaPlanApprove implements AtlasExec.
func (m *mockAtlas) SchemaPlanApprove(ctx context.Context, p *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error) {
	return m.schemaPlanApprove(ctx, p)
}

// SchemaPlanLint implements AtlasExec.
func (m *mockAtlas) SchemaPlanLint(ctx context.Context, p *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error) {
	return m.schemaPlanLint(ctx, p)
}

// SchemaPlanPull implements AtlasExec.
func (m *mockAtlas) SchemaPlanPull(ctx context.Context, p *atlasexec.SchemaPlanPullParams) (string, error) {
	return m.schemaPlanPull(ctx, p)
}

func (m *mockAtlas) MigrateDown(ctx context.Context, params *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error) {
	return m.migrateDown(ctx, params)
}

func TestMigratePush(t *testing.T) {
	t.Run("config-broken", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/broken.hcl")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.ErrorContains(t, err, `"testdata/config/broken.hcl" was not found`)
	})
	t.Run("env-broken", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "broken-env")
		tt.setInput("dir-name", "test-dir")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.ErrorContains(t, err, `env "broken-env" not defined in config file`)
	})
	t.Run("broken dir", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("dir", "file://some_broken_dir")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.ErrorContains(t, err, `sql/migrate: stat some_broken_dir: no such file or directory`)
	})
	t.Run("broken latest", func(t *testing.T) {
		if os.Getenv("BE_CRASHER") == "1" {
			// Reset the output to stdout
			tt := newT(t, githubactions.WithWriter(os.Stdout))
			tt.setInput("dir", "file://testdata/migrations")
			tt.setInput("dir-name", "test-dir")
			tt.setInput("latest", "foo")
			tt.setInput("dev-url", "sqlite://file?mode=memory")
			_ = (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
			return
		}
		var out bytes.Buffer
		// Run the test command with the BE_CRASHER environment variable set to cause a crash.
		// https://stackoverflow.com/a/33404435
		cmd := exec.Command(os.Args[0], "-test.v", "-test.run="+t.Name())
		cmd.Env = append(os.Environ(), "BE_CRASHER=1")
		cmd.Stdout = &out
		err := cmd.Run()
		require.Error(t, err)
		require.Contains(t, out.String(), `::error::the input "latest" got invalid value for boolean`)
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.ErrorContains(t, err, `unknown driver "broken-driver"`)
	})
	t.Run("invalid tag", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "invalid-character@")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.ErrorContains(t, err, `tag must be lowercase alphanumeric`)
	})
	t.Run("tag", func(t *testing.T) {
		tt := newT(t)
		tt.cli, _ = atlasexec.NewClient("", "./mock-push.sh")
		os.Remove("push-out.txt")
		t.Cleanup(func() {
			os.Remove("push-out.txt")
		})
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "valid-tag-123")
		tt.setInput("latest", "true")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.NoError(t, err)
		out, err := os.ReadFile("push-out.txt")
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		require.Len(t, lines, 2)
		require.Contains(t, lines[0], "test-dir")
		require.NotContains(t, lines[0], "valid-tag-123")
		require.Contains(t, lines[1], "test-dir:valid-tag-123")
	})
	t.Run("no latest", func(t *testing.T) {
		tt := newT(t)
		tt.cli, _ = atlasexec.NewClient("", "./mock-push.sh")
		os.Remove("push-out.txt")
		t.Cleanup(func() {
			os.Remove("push-out.txt")
		})
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "valid-tag-123")
		tt.setInput("latest", "false")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.NoError(t, err)
		out, err := os.ReadFile("push-out.txt")
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		require.Len(t, lines, 1)
		require.Contains(t, lines[0], "test-dir:valid-tag-123")
	})
	t.Run("config", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("env", "test")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir-name", "test-dir")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.NoError(t, err)
	})
	t.Run("dir-name invalid characters", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-#dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
		require.ErrorContains(t, err, "slug must be lowercase alphanumeric")
	})
}

func TestMigrateTest(t *testing.T) {
	t.Run("all inputs", func(t *testing.T) {
		tt := newT(t)
		tt.cli, _ = atlasexec.NewClient("", "./mock-atlas-test.sh")
		os.Remove("test-out.txt")
		t.Cleanup(func() {
			os.Remove("test-out.txt")
		})
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("run", "example")
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		tt.setInput("vars", `{"var1": "value1", "var2": "value2"}`)
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateTest(context.Background())
		require.NoError(t, err)
		out, err := os.ReadFile("test-out.txt")
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		require.Len(t, lines, 1)
		require.Contains(t, lines[0], "--env test")
		require.Contains(t, lines[0], "--run example")
		require.Contains(t, lines[0], "--var var1=value1")
		require.Contains(t, lines[0], "--var var2=value2")
		require.Contains(t, lines[0], "--dir file://testdata/migrations")
		require.Contains(t, lines[0], "--dev-url sqlite://file?mode=memory")
	})
}

func TestSchemaTest(t *testing.T) {
	t.Run("all inputs", func(t *testing.T) {
		tt := newT(t)
		tt.cli, _ = atlasexec.NewClient("", "./mock-atlas-test.sh")
		os.Remove("test-out.txt")
		t.Cleanup(func() {
			os.Remove("test-out.txt")
		})
		tt.setInput("url", "file://schema.hcl")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("run", "example")
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		tt.setInput("vars", `{"var1": "value1", "var2": "value2"}`)
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).SchemaTest(context.Background())
		require.NoError(t, err)
		out, err := os.ReadFile("test-out.txt")
		require.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		require.Len(t, lines, 1)
		require.Contains(t, lines[0], "--env test")
		require.Contains(t, lines[0], "--run example")
		require.Contains(t, lines[0], "--var var1=value1")
		require.Contains(t, lines[0], "--var var2=value2")
		require.Contains(t, lines[0], "--url file://schema.hcl")
		require.Contains(t, lines[0], "--dev-url sqlite://file?mode=memory")
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
	tt.setInput("latest", "true")
	tt.env["GITHUB_REPOSITORY"] = "repository"
	tt.env["GITHUB_HEAD_REF"] = "testing-branch"
	tt.env["GITHUB_REF_NAME"] = "refs/pulls/6/merge"
	tt.env["GITHUB_SHA"] = "sha1234"
	tt.env["GITHUB_ACTOR"] = "test-user"
	tt.env["GITHUB_ACTOR_ID"] = "123"
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
	var err error
	err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, len(payloads))
	require.Equal(t, "test-dir", payloads[0].SyncDir.Slug)
	require.Equal(t, expected, payloads[0].SyncDir.Context)
	require.Equal(t, payloads[1].PushDir.Tag, "sha1234")
	require.Equal(t, payloads[1].PushDir.Slug, "test-dir")
	tt.env["GITHUB_HEAD_REF"] = ""
	err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigratePush(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "required flag(s) \"dev-url\" not set")
	})
	t.Run("lint - missing dir", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir-name", "test-dir-slug")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "stat migrations: no such file or directory")
	})
	t.Run("lint - bad dir name", func(t *testing.T) {
		tt := newT(t)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "missing required parameter dir-name")
		tt.setInput("dir-name", "fake-dir-name")
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, `dir "fake-dir-name" not found`)
		tt.setInput("dir-name", "atlas://test-dir-slug") // user must not add atlas://
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>testdata/migrations_destructive</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
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
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Len(t, comments, 1)
		require.Equal(t, "updated comment", comments[0].Body)
	})
	t.Run("lint summary - no text edit", func(t *testing.T) {
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
				// language=JSON
				_, err := writer.Write([]byte(`[{"filename": "testdata/drop_column/20240626085256_init.sql"}, {"filename": "testdata/drop_column/20240626085324_drop_col.sql"}]`))
				require.NoError(t, err)
			default:
				writer.WriteHeader(http.StatusNotFound)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/drop_column")
		tt.setInput("dir-name", "test-dir-slug")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>testdata/drop_column</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "error file=testdata/drop_column/20240626085324_drop_col.sql")
		require.Contains(t, out, "destructive changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#DS103")
		// There is no suggestion for dropping a column because there are 2 statements in the file
		require.Len(t, comments, 0)
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>migrations_destructive</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
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
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Len(t, comments, 1)
		require.Equal(t, "updated comment", comments[0].Body)
	})
	t.Run("lint summary - lint error - github api not working", func(t *testing.T) {
		tt := newT(t)
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
			// List comments endpoint
			if path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet {
				// API is not working
				writer.WriteHeader(http.StatusUnprocessableEntity)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>migrations_destructive</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "error file=testdata/migrations_destructive/20230925192914.sql")
		require.Contains(t, out, "destructive changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#DS102")
		require.Len(t, comments, 0)
	})
	t.Run("lint summary - lint error - push event", func(t *testing.T) {
		tt := newT(t)
		tt.env["GITHUB_EVENT_NAME"] = "push"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "`atlas migrate lint` completed with errors, see report: https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		// The summary should be create for push event
		require.NotEmpty(t, string(c))
		require.NotEmpty(t, tt.out.String())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "`atlas migrate lint` on <strong>testdata/diagnostics</strong>\n")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.NoError(t, err)
		require.Equal(t, 3, len(ghPayloads))
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
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Equal(t, 8, len(ghPayloads))
		found = slices.IndexFunc(ghPayloads, func(gh ghPayload) bool {
			if gh.Method != http.MethodPost {
				return false
			}
			if !strings.Contains(gh.Body, "1 reports were found in analysis") {
				return false
			}
			return strings.Contains(gh.Body, "generated by ariga/atlas-action for test-dir-slug")
		})
		require.NotEqual(t, -1, found)
		// Run Lint against a directory that has an existing comment, expecting a PATCH
		tt.setInput("dir-name", "other-dir-slug")
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Equal(t, 13, len(ghPayloads))
		found = slices.IndexFunc(ghPayloads, func(gh ghPayload) bool {
			if gh.Method != http.MethodPatch {
				return false
			}
			if !strings.Contains(gh.Body, "1 reports were found in analysis") {
				return false
			}
			return strings.Contains(gh.Body, "generated by ariga/atlas-action for other-dir-slug")
		})
		require.NotEqual(t, -1, found)
		// Run Lint with input errors, no calls to github api should be made
		tt.setInput("dir-name", "fake-dir-name")
		err = (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateLint(context.Background())
		require.Equal(t, 13, len(ghPayloads))
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
				Steps: []*atlasexec.StepReport{
					{
						Name: "Migration Integrity Check",
						Text: "File atlas.sum is valid",
					},
					{
						Name: "Detect New Migration Files",
						Text: "Found 1 new migration files (from 1 total)",
					},
				},
				Env: env{
					Dir: "testdata/migrations",
				},
				Files: []*atlasexec.FileReport{{Name: "20230925192914.sql"}},
			},
			// language=html
			expected: "`atlas migrate lint`" + ` on <strong>testdata/migrations</strong>
<table>
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
            <td>20230925192914.sql
            </td>
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
				Steps: []*atlasexec.StepReport{
					{
						Name: "Migration Integrity Check",
						Text: "File atlas.sum is valid",
					},
					{
						Name: "Detect New Migration Files",
						Text: "Found 1 new migration files (from 1 total)",
					},
					{
						Name: "Analyze 20230925192914.sql",
						Text: "2 reports were found in analysis",
						Result: &atlasexec.FileReport{
							Name: "20230925192914.sql",
							Text: "CREATE UNIQUE INDEX idx_unique_fullname ON Persons (FirstName, LastName);\nALTER TABLE Persons ADD City varchar(255) NOT NULL;\n",
							Reports: []sqlcheck.Report{
								{
									Text: "data dependent changes detected",
									Diagnostics: []sqlcheck.Diagnostic{
										{
											Text: "Adding a unique index \"idx_unique_fullname\" on table \"Persons\" might fail in case columns \"FirstName\", \"LastName\" contain duplicate entries",
											Code: "MF101",
										},
										{
											Text: "Adding a non-nullable \"varchar\" column \"City\" on table \"Persons\" without a default value implicitly sets existing rows with \"\"",
											Code: "MY101",
										},
									},
								},
							},
						},
					},
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
			expected: "`atlas migrate lint`" + ` on <strong>testdata/migrations</strong>
<table>
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
            <td>20230925192914.sql
            </td>
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
                Analyze 20230925192914.sql <br/> 2 reports were found in analysis
            </td>
            <td>
                <b>Data dependent changes detected</b><br/>
                Adding a unique index "idx_unique_fullname" on table "Persons" might fail in case columns "FirstName", "LastName" contain duplicate entries <a href="https://atlasgo.io/lint/analyzers#MF101" target="_blank">(MF101)</a><br/>
                Adding a non-nullable "varchar" column "City" on table "Persons" without a default value implicitly sets existing rows with "" <a href="https://atlasgo.io/lint/analyzers#MY101" target="_blank">(MY101)</a><br/>
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
				Steps: []*atlasexec.StepReport{
					{
						Name: "Migration Integrity Check",
						Text: "File atlas.sum is valid",
					},
					{
						Name: "Detect New Migration Files",
						Text: "Found 1 new migration files (from 1 total)",
					},
					{
						Name: "Analyze 20230925192914.sql",
						Text: "1 reports were found in analysis",
						Result: &atlasexec.FileReport{
							Name: "20230925192914.sql",
							Text: "CREATE UNIQUE INDEX idx_unique_fullname ON Persons (FirstName, LastName);",
							Reports: []sqlcheck.Report{
								{
									Text: "data dependent changes detected",
									Diagnostics: []sqlcheck.Diagnostic{
										{
											Text: "Adding a unique index \"idx_unique_fullname\" on table \"Persons\" might fail in case columns \"FirstName\", \"LastName\" contain duplicate entries",
											Code: "MF101",
										},
									},
								},
							},
						},
					},
					{
						Name: "Analyze 20240625104520_destructive.sql",
						Text: "1 reports were found in analysis",
						Result: &atlasexec.FileReport{
							Error: "Destructive changes detected",
							Name:  "20240625104520_destructive.sql",
							Text:  "DROP TABLE Persons;\n\n",
							Reports: []sqlcheck.Report{
								{
									Text: "destructive changes detected",
									Diagnostics: []sqlcheck.Diagnostic{
										{
											Text: "Dropping table \"Persons\"",
											Code: "DS102",
										},
									},
								},
							},
						},
					},
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
			expected: "`atlas migrate lint`" + ` on <strong>testdata/migrations</strong>
<table>
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
            <td>20230925192914.sql<br/>20230925192915.sql
            </td>
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
                Analyze 20230925192914.sql <br/> 1 reports were found in analysis
            </td>
            <td>
                <b>Data dependent changes detected</b><br/>
                Adding a unique index "idx_unique_fullname" on table "Persons" might fail in case columns "FirstName", "LastName" contain duplicate entries <a href="https://atlasgo.io/lint/analyzers#MF101" target="_blank">(MF101)</a><br/>
            </td>
        </tr>
        <tr>
            <td>
                <div align="center">
                    <img width="20px" height="21px" src="https://release.ariga.io/images/assets/error.svg"/>
                </div>
            </td>
            <td>
                Analyze 20240625104520_destructive.sql <br/> 1 reports were found in analysis
            </td>
            <td>
                <b>Destructive changes detected</b><br/>
                Dropping table "Persons" <a href="https://atlasgo.io/lint/analyzers#DS102" target="_blank">(DS102)</a><br/>
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
				Steps: []*atlasexec.StepReport{
					{
						Name:  "Migration Integrity Check",
						Text:  "File atlas.sum is invalid",
						Error: "checksum mismatch",
					},
				},
				Files: []*atlasexec.FileReport{{
					Name:  "20230925192914.sql",
					Error: "checksum mismatch",
				}},
			},
			// language=html
			expected: "`atlas migrate lint`" + ` on <strong>testdata/migrations</strong>
<table>
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
            <td>20230925192914.sql
            </td>
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
                Migration Integrity Check <br/> File atlas.sum is invalid
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
		{
			name: "non linear history error",
			payload: &atlasexec.SummaryReport{
				URL: "https://migration-lint-report-url",
				Env: env{
					Dir: "testdata/migrations",
				},
				Steps: []*atlasexec.StepReport{
					{
						Name: "Migration Integrity Check",
						Text: "File atlas.sum is valid",
					},
					{
						Name: "Detected 1 non-additive change",
						Text: "Pulling the the latest git changes might fix this warning",
						Result: &atlasexec.FileReport{
							Reports: []sqlcheck.Report{
								{
									Diagnostics: []sqlcheck.Diagnostic{
										{
											Pos:  0,
											Text: "File 20240613102407.sql is missing or has been removed. Changes that have already been applied will not be reverted",
										},
									},
								},
							},
						},
					},
				},
			},
			// language=html
			expected: "`atlas migrate lint`" + ` on <strong>testdata/migrations</strong>
<table>
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
                No migration files detected
            </td>
            <td>&nbsp;
            </td>
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
                Detected 1 non-additive change <br/> Pulling the the latest git changes might fix this warning
            </td>
            <td>
                File 20240613102407.sql is missing or has been removed. Changes that have already been applied will not be reverted<br/>
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
			c, err := migrateLintComment(tt.payload)
			require.NoError(t, err)
			require.Equal(t, tt.expected, c)
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
			name: "first apply, 2 files, 3 statements",
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
				Target: "20221108173658",
				Start:  must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.909446+03:00")),
				End:    must(time.Parse(time.RFC3339, "2024-06-16T15:27:38.963743+03:00")),
			},
			// language=markdown
			expected: "<h2>\n    <img height=\"17\" src=\"https://release.ariga.io/images/assets/success.svg\"/> Migration Passed\n</h2>\n\n#### `atlas migrate apply` Summary:\n\n<table>\n    <tr>\n        <th>Parameter</th>\n        <th>Details</th>\n    </tr>\n    <tr>\n        <td>Migration Directory</td>\n        <td><code>testdata/migrations</code></td>\n    </tr>\n    <tr>\n        <td>Database URL</td>\n        <td><code>sqlite://file?_fk=1&mode=memory</code></td>\n    </tr>\n    <tr>\n        <td>Migrate to Version</td>\n       <td>\n        <code>20221108173658</code>\n       </td>\n    </tr>\n    <tr>\n        <td>SQL Summary</td>\n        <td>2 migration files, 3 statements passed</td>\n    </tr>\n    <tr>\n        <td>Total Time</td>\n        <td>54.297ms</td>\n    </tr>\n</table>\n\n#### Version 20221108173626.sql:\n<table>\n    <tr>\n        <th>Status</th>\n        <th>Executed Statements</th>\n        <th>Execution Time</th>\n        <th>Error</th>\n        <th>Error Statement</th>\n    </tr>\n    <tr>\n        <td>\n        <div align=\"center\">\n            <img width=\"20px\" height=\"21px\" src=\"https://release.ariga.io/images/assets/success.svg\"/>\n        </div>\n        </td>\n        <td>2</td>\n        <td>25.765ms</td>\n        <td>-</td>\n        <td>-</td>\n    </tr>\n</table>\n\n<details>\n<summary>📄 View SQL Statements</summary>\n\n```sql\nCREATE TABLE `dept_emp_latest_date` (`emp_no` int NOT NULL, `from_date` date NULL, `to_date` date NULL) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci COMMENT \"VIEW\";\nCREATE TABLE `employees` (`emp_no` int NOT NULL, `birth_date` date NOT NULL, `first_name` varchar(14) NOT NULL, `last_name` varchar(16) NOT NULL, `gender` enum('M','F') NOT NULL, `hire_date` date NOT NULL, PRIMARY KEY (`emp_no`)) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n```\n</details>\n\n\n\n#### Version 20221108173658.sql:\n<table>\n    <tr>\n        <th>Status</th>\n        <th>Executed Statements</th>\n        <th>Execution Time</th>\n        <th>Error</th>\n        <th>Error Statement</th>\n    </tr>\n    <tr>\n        <td>\n        <div align=\"center\">\n            <img width=\"20px\" height=\"21px\" src=\"https://release.ariga.io/images/assets/success.svg\"/>\n        </div>\n        </td>\n        <td>1</td>\n        <td>23.4ms</td>\n        <td>-</td>\n        <td>-</td>\n    </tr>\n</table>\n\n<details>\n<summary>📄 View SQL Statements</summary>\n\n```sql\nCREATE TABLE `employees` (`emp_no` int NOT NULL, `birth_date` date NOT NULL, `first_name` varchar(14) NOT NULL, `last_name` varchar(16) NOT NULL, `gender` enum('M','F') NOT NULL, `hire_date` date NOT NULL, PRIMARY KEY (`emp_no`)) CHARSET utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n```\n</details>\n",
		},
		{
			name: "2 files, 1 statement error",
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
							Stmt string
							Text string
						}{
							Stmt: "create Table Err?",
							Text: "Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near '?' at line 1",
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
			expected: "<h2>\n    <img height=\"17\" src=\"https://release.ariga.io/images/assets/error.svg\"/> Migration Failed\n</h2>\n\n#### `atlas migrate apply` Summary:\n\n<table>\n    <tr>\n        <th>Parameter</th>\n        <th>Details</th>\n    </tr>\n    <tr>\n        <td>Migration Directory</td>\n        <td><code>testdata/migrations</code></td>\n    </tr>\n    <tr>\n        <td>Database URL</td>\n        <td><code>mysql://localhost:3306/test?parseTime=true</code></td>\n    </tr>\n    <tr>\n        <td>Migrate from Version</td>\n        <td><code>20221108143624</code></td>\n    </tr>\n    <tr>\n        <td>Migrate to Version</td>\n       <td>\n        <code>20221108173658</code>\n       </td>\n    </tr>\n    <tr>\n        <td>SQL Summary</td>\n        <td>2 migration files, 2 statements passed, 1 failed</td>\n    </tr>\n    <tr>\n        <td>Total Time</td>\n        <td>54.297ms</td>\n    </tr>\n</table>\n\n#### Version 20221108173626.sql:\n<table>\n    <tr>\n        <th>Status</th>\n        <th>Executed Statements</th>\n        <th>Execution Time</th>\n        <th>Error</th>\n        <th>Error Statement</th>\n    </tr>\n    <tr>\n        <td>\n        <div align=\"center\">\n            <img width=\"20px\" height=\"21px\" src=\"https://release.ariga.io/images/assets/success.svg\"/>\n        </div>\n        </td>\n        <td>1</td>\n        <td>25.765ms</td>\n        <td>-</td>\n        <td>-</td>\n    </tr>\n</table>\n\n<details>\n<summary>📄 View SQL Statements</summary>\n\n```sql\nCREATE TABLE Persons ( PersonID int );\n```\n</details>\n\n\n\n#### Version 20221108173658.sql:\n<table>\n    <tr>\n        <th>Status</th>\n        <th>Executed Statements</th>\n        <th>Execution Time</th>\n        <th>Error</th>\n        <th>Error Statement</th>\n    </tr>\n    <tr>\n        <td>\n        <div justify-content=\"center\">\n            <img width=\"20px\" height=\"21px\" src=\"https://release.ariga.io/images/assets/error.svg\"/>\n        </div>\n        </td>\n        <td>1</td>\n        <td>23.4ms</td>\n        <td>Error 1064 (42000): You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near '?' at line 1</td>\n        <td><details><summary>📄 View</summary><pre><code>create Table Err?</code></pre></details></td>\n    </tr>\n</table>\n\n<details>\n<summary>📄 View SQL Statements</summary>\n\n```sql\ncreate Table Err?\n```\n</details>\n",
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
				End:     must(time.Parse(time.RFC3339, "2024-06-16T16:09:01.689411+03:00")),
			},
			expected: "<h2>\n    <img height=\"17\" src=\"https://release.ariga.io/images/assets/success.svg\"/> Migration Passed\n</h2>\n\n#### `atlas migrate apply` Summary:\n\n<table>\n    <tr>\n        <th>Parameter</th>\n        <th>Details</th>\n    </tr>\n    <tr>\n        <td>Migration Directory</td>\n        <td><code>testdata/migrations</code></td>\n    </tr>\n    <tr>\n        <td>Database URL</td>\n        <td><code>mysql://localhost:3306/test?parseTime=true</code></td>\n    </tr>\n    <tr>\n        <td>Migrate from Version</td>\n        <td><code>20240616130838</code></td>\n    </tr>\n    <tr>\n        <td>Migrate to Version</td>\n       <td>\n        <code>20240616130838</code>\n       </td>\n    </tr>\n    <tr>\n        <td>SQL Summary</td>\n        <td>0 migration files</td>\n    </tr>\n    <tr>\n        <td>Total Time</td>\n        <td>5.64ms</td>\n    </tr>\n</table>",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := migrateApplyComment(tt.payload)
			require.NoError(t, err)
			require.Contains(t, c, tt.expected)
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
	c := generateHCL(t, url, token)
	tt.setInput("config", c)
	tt.configUrl = c
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
		err := (&Actions{Action: tt.act, Atlas: tt.cli, Version: "v1.2.3"}).MigrateApply(context.Background())
		require.NoError(t, err)

		require.Len(t, payloads, 3)
		require.Contains(t, payloads[0], "query Bot")
		require.Contains(t, payloads[1], "query dirState")
		require.Contains(t, payloads[2], "mutation ReportMigration")
		require.Contains(t, payloads[2], `"context":{"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"}`)

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n       <td>\n        <code>20230922132634</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "1 migration file, 1 statement passed")
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

		err := (&Actions{Action: tt.act, Atlas: tt.cli}).MigrateApply(context.Background())
		require.NoError(t, err)

		require.Len(t, payloads, 2)
		require.Contains(t, payloads[0], "query Bot")
		require.Contains(t, payloads[1], "query dirState")

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n       <td>\n        <code>20230922132634</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "1 migration file, 1 statement passed")
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
	db        string
	env       map[string]string
	out       bytes.Buffer
	cli       AtlasExec
	act       Action
	configUrl string
}

func newT(t *testing.T, opts ...githubactions.Option) *test {
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
	opts = append([]githubactions.Option{
		githubactions.WithGetenv(func(key string) string {
			return tt.env[key]
		}),
		githubactions.WithWriter(&tt.out),
	}, opts...)
	tt.act = NewGHAction(opts...)
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

func Test_mergedPullRequest(t *testing.T) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Skip("Missing GITHUB_TOKEN")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &oauth2.Transport{
			Source: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: token,
			}),
		},
	}
	c := &githubAPI{
		client:  client,
		gql:     githubv4.NewClient(client),
		baseURL: defaultGHApiUrl,
		repo:    "ariga/atlas-action",
	}
	pr, err := c.MergedPullRequest(context.Background(), "6850844a4bb6933f11ee941ca232fd636f35a35f")
	require.NoError(t, err)
	require.NotNil(t, pr)
	require.Equal(t, 206, pr.Number)
}

func TestSchema_Plan(t *testing.T) {
	var (
		graphqlCounter int
		commentCounter int
		commentEdited  int
		noPullRequest  bool
	)
	h := http.NewServeMux()
	h.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		var req map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, map[string]any{
			"commit": "commit-id",
			"name":   "atlas-action",
			"owner":  "ariga",
		}, req["variables"])
		graphqlCounter++
		if noPullRequest {
			fmt.Fprint(w, `{"data":{"repository":{"object":{"associatedPullRequests":{"nodes":[]}}}}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"repository":{"object":{"associatedPullRequests":{"nodes":[{"number":1,"URL":"https://github.com/ariga/atlas-action/pull/1"}]}}}}}`)
	})
	h.HandleFunc("GET /repos/ariga/atlas-action/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		if commentCounter == 0 {
			fmt.Fprint(w, `[]`) // No comments
		} else { // Existing comment
			fmt.Fprintf(w, `[{"id":1,"body":"%s"}]`, commentMarker("ufnTS7NrAgkvQlxbpnSxj119MAPGNqVj0i3Eelv+iLc="))
		}
	})
	h.HandleFunc("POST /repos/ariga/atlas-action/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		commentCounter++
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{}`)
	})
	h.HandleFunc("PATCH /repos/ariga/atlas-action/issues/comments/1", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		commentEdited++
		fmt.Fprint(w, `{}`)
	})
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	plan := &atlasexec.SchemaPlan{
		Repo: "atlas-action",
		File: &atlasexec.SchemaPlanFile{
			FromHash: "ufnTS7NrAgkvQlxbpnSxj119MAPGNqVj0i3Eelv+iLc=", // Used as comment marker
			ToHash:   "Rl4lBdMkvFoGQ4xu+3sYCeogTVnamJ7bmDoq9pMXcjw=", // Apart of the plan URL
		},
		Lint: &atlasexec.SummaryReport{
			Files: []*atlasexec.FileReport{},
		},
	}
	var planErr, pullErr, approveErr error
	m := &mockAtlas{
		schemaPlan: func(_ context.Context, p *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error) {
			// Common input checks
			require.Equal(t, "file://atlas.hcl", p.ConfigURL)
			require.Equal(t, "test", p.Env)
			require.Equal(t, "", p.Repo) // No repo, provided by atlas.hcl
			if planErr != nil {
				return nil, planErr
			}
			return plan, nil
		},
		schemaPlanPull: func(_ context.Context, p *atlasexec.SchemaPlanPullParams) (string, error) {
			// Common input checks
			require.Equal(t, "file://atlas.hcl", p.ConfigURL)
			require.Equal(t, "test", p.Env)
			require.Equal(t, "atlas://atlas-action/plans/pr-1-Rl4lBdMk", p.URL)
			return "", pullErr
		},
		schemaPlanLint: func(_ context.Context, p *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error) {
			// Common input checks
			require.Equal(t, "file://atlas.hcl", p.ConfigURL)
			require.Equal(t, "test", p.Env)
			require.Equal(t, "", p.Repo) // No repo, provided by atlas.hcl
			require.Equal(t, "atlas://atlas-action/plans/pr-1-Rl4lBdMk", p.File)
			return plan, nil
		},
		schemaPlanApprove: func(_ context.Context, p *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error) {
			require.Equal(t, "file://atlas.hcl", p.ConfigURL)
			require.Equal(t, "atlas://atlas-action/plans/pr-1-Rl4lBdMk", p.URL)
			if approveErr != nil {
				return nil, approveErr
			}
			return &atlasexec.SchemaPlanApprove{
				URL:    "atlas://atlas-action/plans/pr-1-Rl4lBdMk",
				Link:   "https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk",
				Status: "APPROVED",
			}, nil
		},
	}
	t.Setenv("GITHUB_TOKEN", "token")
	out := &bytes.Buffer{}
	act := &mockAction{
		inputs: map[string]string{
			// "schema-name": "atlas://atlas-action",
			"from":   "sqlite://file?_fk=1&mode=memory",
			"config": "file://atlas.hcl",
			"env":    "test",
		},
		logger: slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey {
					return slog.String(slog.TimeKey, "NOW") // Fake time
				}
				return a
			},
		})),
		trigger: &TriggerContext{
			SCM:     SCM{Type: atlasexec.SCMTypeGithub, APIURL: srv.URL},
			Repo:    "ariga/atlas-action",
			RepoURL: "https://github.com/ariga/atlas-action",
			Branch:  "g/feature-1",
			Commit:  "commit-id",
			PullRequest: &PullRequest{
				Number: 1,
				URL:    "https://github.com/ariga/atlas-action/pull/1",
				Commit: "commit-id",
			},
		},
	}
	ctx := context.Background()
	// No changes
	planErr = errors.New("The current state is synced with the desired state, no changes to be made")
	require.NoError(t, (&Actions{Action: act, Atlas: m}).SchemaPlan(ctx))
	require.Len(t, act.summary, 0, "no summaries generated")
	require.Equal(t, 0, commentCounter, "expected 1 comment generated")
	require.Equal(t, 0, commentEdited, "No comment should be edited")

	// No existing plan
	planErr = nil
	pullErr = errors.New(`plan "pr-1-Rl4lBdMk" was not found`)
	require.NoError(t, (&Actions{Action: act, Atlas: m}).SchemaPlan(ctx))
	require.Len(t, act.summary, 1, "expected 1 summary")
	require.Equal(t, 1, commentCounter, "expected 1 comment generated")
	require.Equal(t, 0, commentEdited, "No comment should be edited")

	// Existing plan
	pullErr = nil
	require.NoError(t, (&Actions{Action: act, Atlas: m}).SchemaPlan(ctx))
	require.Len(t, act.summary, 2, "expected 2 summaries")
	require.Equal(t, 1, commentCounter, "No more comments generated")
	require.Equal(t, 1, commentEdited, "Expected comment to be edited")

	// Trigger with no pull request, master branch
	act.trigger.PullRequest = nil
	act.trigger.Branch = "master"
	require.NoError(t, (&Actions{Action: act, Atlas: m}).SchemaPlan(ctx))
	require.Len(t, act.summary, 2, "no more summaries generated")
	require.Equal(t, 1, commentCounter, "No more comments generated")
	require.Equal(t, 1, commentEdited, "No comment should be edited")

	// No pending plan
	approveErr = errors.New(`plan "pr-1-Rl4lBdMk" was not found`)
	require.NoError(t, (&Actions{Action: act, Atlas: m}).SchemaPlan(ctx))
	require.Len(t, act.summary, 2, "no more summaries generated")
	require.Equal(t, 1, commentCounter, "No more comments generated")
	require.Equal(t, 1, commentEdited, "No comment should be edited")

	// No pull request found for commit
	noPullRequest = true
	require.NoError(t, (&Actions{Action: act, Atlas: m}).SchemaPlan(ctx))
	require.Len(t, act.summary, 2, "no more summaries generated")
	require.Equal(t, 1, commentCounter, "No more comments generated")
	require.Equal(t, 1, commentEdited, "No comment should be edited")

	// Check all logs output
	require.Equal(t, `time=NOW level=INFO msg="Schema plan completed successfully, no changes to be made"
time=NOW level=INFO msg="Schema plan does not exist, creating a new one with name \"pr-1-Rl4lBdMk\""
time=NOW level=INFO msg="Schema plan already exists, linting the plan \"pr-1-Rl4lBdMk\""
time=NOW level=INFO msg="Schema plan approved successfully: https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk"
time=NOW level=INFO msg="Schema plan does not exist \"pr-1-Rl4lBdMk\""
time=NOW level=INFO msg="No merged pull request found for commit \"commit-id\", skip the approval"
`, out.String())
}

type mockAction struct {
	trigger *TriggerContext   // trigger context
	inputs  map[string]string // input values
	output  map[string]string // step's output
	summary []string          // step summaries
	logger  *slog.Logger      // logger
	fatal   bool              // fatal called
}

var _ Action = (*mockAction)(nil)

// GetType implements Action.
func (m *mockAction) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGithubAction
}

// GetTriggerContext implements Action.
func (m *mockAction) GetTriggerContext() (*TriggerContext, error) {
	return m.trigger, nil
}

// GetInput implements Action.
func (m *mockAction) GetInput(name string) string {
	return m.inputs[name]
}

// SetOutput implements Action.
func (m *mockAction) SetOutput(name, value string) {
	m.output[name] = value
}

// AddStepSummary implements Action.
func (m *mockAction) AddStepSummary(s string) {
	m.summary = append(m.summary, s)
}

// Infof implements Action.
func (m *mockAction) Infof(msg string, args ...interface{}) {
	m.logger.Info(fmt.Sprintf(msg, args...))
}

// Warningf implements Action.
func (m *mockAction) Warningf(msg string, args ...interface{}) {
	m.logger.Warn(fmt.Sprintf(msg, args...))
}

// Errorf implements Action.
func (m *mockAction) Errorf(msg string, args ...interface{}) {
	m.logger.Error(fmt.Sprintf(msg, args...))
}

// Fatalf implements Action.
func (m *mockAction) Fatalf(msg string, args ...interface{}) {
	m.Errorf(msg, args...)
	m.fatal = true // Mark fatal called
}

// WithFieldsMap implements Action.
func (m *mockAction) WithFieldsMap(args map[string]string) Logger {
	argPairs := make([]any, 0, len(args)*2)
	for k, v := range args {
		argPairs = append(argPairs, k, v)
	}
	return &mockAction{
		inputs:  m.inputs,
		trigger: m.trigger,
		output:  m.output,
		summary: m.summary,
		fatal:   m.fatal,
		logger:  m.logger.With(argPairs...),
	}
}
