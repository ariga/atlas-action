// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-action/atlasaction/cloud"
	"ariga.io/atlas-action/internal/cmdapi"
	"ariga.io/atlas-go-sdk/atlasexec"
	"ariga.io/atlas/sql/migrate"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rogpeppe/go-internal/diff"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

func TestMigrateApply(t *testing.T) {
	t.Run("local dir", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n    <td>\n      <code>20230922132634</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "1 migration file, 1 statement passed")
	})
	t.Run("broken migration dir", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/broken/")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.EqualError(t, err, "sql/migrate: executing statement \"CREATE TABLE OrderDetails (\\n    OrderDetailID INTEGER PRIMARY KEY AUTOINCREMENT,\\n    OrderID INTEGER-\\n);\" from version \"20240619073319\": near \"-\": syntax error")

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n    <td>\n      <code>20240619073319</code>")
		require.Contains(t, string(c), "Migration Failed")
		require.Contains(t, string(c), "2 migration files, 3 statements passed, 1 failed")

	})
	t.Run("dry-run", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("dry-run", "true")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)
		stat, err := tt.cli.MigrateStatus(context.Background(), &atlasexec.MigrateStatusParams{
			URL:    "sqlite://" + tt.db,
			DirURL: "file://testdata/migrations/",
		})
		require.NoError(t, err)
		require.Empty(t, stat.Applied)
	})
	t.Run("dry-run false", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("dry-run", "false")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)
		stat, err := tt.cli.MigrateStatus(context.Background(), &atlasexec.MigrateStatusParams{
			URL:    "sqlite://" + tt.db,
			DirURL: "file://testdata/migrations/",
		})
		require.NoError(t, err)
		require.Len(t, stat.Applied, 1)
	})
	t.Run("tx-mode", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("tx-mode", "fake")
		err := tt.newActs(t).MigrateApply(context.Background())

		// The error here proves that the tx-mode was passed to atlasexec, which
		// is what we want to test.
		exp := `unknown tx-mode "fake"`
		require.ErrorContains(t, err, exp)
		m, err := tt.outputs()
		require.NoError(t, err)
		require.Contains(t, m["error"], exp)
	})
	t.Run("baseline", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("baseline", "111_fake")
		err := tt.newActs(t).MigrateApply(context.Background())
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
		tt := newT(t, nil)
		tt.setInput("config", "file://testdata/config/broken.hcl")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.ErrorContains(t, err, `"testdata/config/broken.hcl" was not found`)
	})
	t.Run("config", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)
	})
	t.Run("allow-dirty", func(t *testing.T) {
		tt := newT(t, nil)
		db, err := sql.Open("sqlite3", tt.db)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE dirty_table (id INTEGER PRIMARY KEY)")
		require.NoError(t, err)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		err = tt.newActs(t).MigrateApply(context.Background())
		require.EqualError(t, err, "Error: sql/migrate: connected database is not clean: found multiple tables: 2. baseline version or allow-dirty is required")

		tt.setInput("allow-dirty", "true")
		err = tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)
	})
	t.Run("amount", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations_destructive")
		tt.setInput("amount", "1")
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)
		// Check only one file was applied.
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n    <td>\n      <code>20230922132634</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "1 migration file, 1 statement passed")
	})
}

func TestMigrateDown(t *testing.T) {
	setup := func(t *testing.T) *test {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/down/")
		// Ensure files are applied.
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n    <td>\n      <code>3</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "3 migration files, 3 statements passed")
		tt.resetOut(t)
		tt.setInput("dev-url", "sqlite://dev?mode=memory")
		return tt
	}

	t.Run("down 1 file (default)", func(t *testing.T) {
		tt := setup(t)
		require.NoError(t, tt.newActs(t).MigrateDown(context.Background()))
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
		require.NoError(t, tt.newActs(t).MigrateDown(context.Background()))
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
			require.NoError(t, tt.newActs(t).MigrateDown(context.Background()))
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
			require.NoError(t, tt.newActs(t).MigrateDown(context.Background()))
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
		tt.cli = must(atlasexec.NewClient("", "./mock-atlas.sh"))
		tt.setupConfigWithLogin(t, "", "")
		st := must(json.Marshal(atlasexec.MigrateDown{
			URL:    "URL",
			Status: "PENDING_USER",
		}))
		t.Setenv("TEST_ARGS", fmt.Sprintf(`migrate down --format {{ json . }} --env test --config %s --dev-url sqlite://dev?mode=memory --context {"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"} --url sqlite://%s --dir file://testdata/down/`, tt.configUrl, tt.db))
		t.Setenv("TEST_STDOUT", string(st))
		tt.setInput("env", "test")
		require.EqualError(t, tt.newActs(t).MigrateDown(context.Background()), "plan approval pending, review here: URL")
		require.EqualValues(t, map[string]string{"url": "URL"}, must(tt.outputs()))
	})

	t.Run("aborted", func(t *testing.T) {
		tt := setup(t)
		tt.cli = must(atlasexec.NewClient("", "./mock-atlas.sh"))
		tt.setupConfigWithLogin(t, "", "")
		st := must(json.Marshal(atlasexec.MigrateDown{
			URL:    "URL",
			Status: "ABORTED",
		}))
		t.Setenv("TEST_ARGS", fmt.Sprintf(`migrate down --format {{ json . }} --env test --config %s --dev-url sqlite://dev?mode=memory --context {"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"} --url sqlite://%s --dir file://testdata/down/`, tt.configUrl, tt.db))
		t.Setenv("TEST_STDOUT", string(st))
		t.Setenv("TEST_EXIT_CODE", "1")
		tt.setInput("env", "test")
		require.EqualError(t, tt.newActs(t).MigrateDown(context.Background()), "plan rejected, review here: URL")
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
		actions, err := atlasaction.New(
			atlasaction.WithAction(tt.act),
			atlasaction.WithAtlas(&mockAtlas{
				migrateDown: func(ctx context.Context, params *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error) {
					counter++
					return &atlasexec.MigrateDown{
						URL:    "URL",
						Status: "PENDING_USER",
					}, nil
				},
			}))
		require.NoError(t, err)
		require.EqualError(t, actions.MigrateDown(context.Background()), "plan approval pending, review here: URL")
		require.GreaterOrEqual(t, counter, 3)
	})
}

type mockAtlas struct {
	login             func(context.Context, *atlasexec.LoginParams) error
	migrateDown       func(context.Context, *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error)
	schemaInspect     func(context.Context, *atlasexec.SchemaInspectParams) (string, error)
	schemaPush        func(context.Context, *atlasexec.SchemaPushParams) (*atlasexec.SchemaPush, error)
	schemaPlan        func(context.Context, *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error)
	schemaPlanList    func(context.Context, *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error)
	schemaPlanLint    func(context.Context, *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error)
	schemaPlanApprove func(context.Context, *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error)
}

var _ atlasaction.AtlasExec = (*mockAtlas)(nil)

// Version implements AtlasExec.
func (m *mockAtlas) Version(context.Context) (*atlasexec.Version, error) {
	return &atlasexec.Version{Version: "v0.29.1", SHA: "bb8bf66", Canary: true}, nil
}

// Login implements AtlasExec.
func (m *mockAtlas) Login(ctx context.Context, params *atlasexec.LoginParams) error {
	return m.login(ctx, params)
}

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

// MigrateHash implements AtlasExec.
func (m *mockAtlas) MigrateHash(context.Context, *atlasexec.MigrateHashParams) error {
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

func (m *mockAtlas) SchemaInspect(ctx context.Context, p *atlasexec.SchemaInspectParams) (string, error) {
	return m.schemaInspect(ctx, p)
}

// SchemaPush implements AtlasExec.
func (m *mockAtlas) SchemaPush(ctx context.Context, p *atlasexec.SchemaPushParams) (*atlasexec.SchemaPush, error) {
	return m.schemaPush(ctx, p)
}

// SchemaTest implements AtlasExec.
func (m *mockAtlas) SchemaTest(context.Context, *atlasexec.SchemaTestParams) (string, error) {
	panic("unimplemented")
}

// SchemaPlan implements AtlasExec.
func (m *mockAtlas) SchemaPlan(ctx context.Context, p *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error) {
	return m.schemaPlan(ctx, p)
}

// SchemaPlanList implements AtlasExec.
func (m *mockAtlas) SchemaPlanList(ctx context.Context, p *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error) {
	return m.schemaPlanList(ctx, p)
}

// SchemaPlanApprove implements AtlasExec.
func (m *mockAtlas) SchemaPlanApprove(ctx context.Context, p *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error) {
	return m.schemaPlanApprove(ctx, p)
}

// SchemaPlanLint implements AtlasExec.
func (m *mockAtlas) SchemaPlanLint(ctx context.Context, p *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error) {
	return m.schemaPlanLint(ctx, p)
}

// SchemaPlanStatus implements AtlasExec.
func (m *mockAtlas) SchemaApplySlice(ctx context.Context, params *atlasexec.SchemaApplyParams) ([]*atlasexec.SchemaApply, error) {
	panic("unimplemented")
}

// MigrateDown implements AtlasExec.
func (m *mockAtlas) MigrateDown(ctx context.Context, params *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error) {
	return m.migrateDown(ctx, params)
}

func TestMigratePush(t *testing.T) {
	t.Run("config-broken", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("config", "file://testdata/config/broken.hcl")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		err := tt.newActs(t).MigratePush(context.Background())
		require.ErrorContains(t, err, `"testdata/config/broken.hcl" was not found`)
	})
	t.Run("env-broken", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "broken-env")
		tt.setInput("dir-name", "test-dir")
		err := tt.newActs(t).MigratePush(context.Background())
		require.ErrorContains(t, err, `env "broken-env" not defined in config file`)
	})
	t.Run("broken dir", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setInput("dir", "file://some_broken_dir")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := tt.newActs(t).MigratePush(context.Background())
		require.ErrorContains(t, err, `sql/migrate: stat some_broken_dir: no such file or directory`)
	})
	t.Run("broken latest", func(t *testing.T) {
		if os.Getenv("BE_CRASHER") == "1" {
			// Reset the output to stdout
			tt := newT(t, os.Stdout)
			tt.setInput("dir", "file://testdata/migrations")
			tt.setInput("dir-name", "test-dir")
			tt.setInput("latest", "foo")
			tt.setInput("dev-url", "sqlite://file?mode=memory")
			_ = tt.newActs(t).MigratePush(context.Background())
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
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "broken-driver://")
		err := tt.newActs(t).MigratePush(context.Background())
		require.ErrorContains(t, err, `unknown driver "broken-driver"`)
	})
	t.Run("invalid tag", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "invalid-character@")
		err := tt.newActs(t).MigratePush(context.Background())
		require.ErrorContains(t, err, `tag must be lowercase alphanumeric`)
	})
	t.Run("tag", func(t *testing.T) {
		c, err := atlasexec.NewClient("", "./mock-atlas.sh")
		require.NoError(t, err)
		tt := newT(t, nil)
		tt.cli = c
		tt.setupConfigWithLogin(t, srv.URL, token)

		dir := t.TempDir()
		require.NoError(t, c.SetEnv(map[string]string{"TEST_BATCH": dir}))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "1"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "args"), []byte(fmt.Sprintf(`migrate push --dev-url sqlite://file?mode=memory --dir file://testdata/migrations --context {"path":"file://testdata/migrations","scmType":"GITHUB"} --config %s test-dir`, tt.configUrl)), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "stdout"), []byte("LINK1"), 0644))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "2"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "2", "args"), []byte(fmt.Sprintf(`migrate push --dev-url sqlite://file?mode=memory --dir file://testdata/migrations --context {"path":"file://testdata/migrations","scmType":"GITHUB"} --config %s test-dir:valid-tag-123`, tt.configUrl)), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "2", "stdout"), []byte("LINK2"), 0644))

		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "valid-tag-123")
		tt.setInput("latest", "true")
		require.NoError(t, tt.newActs(t).MigratePush(context.Background()))

		b, err := os.ReadFile(filepath.Join(dir, "counter"))
		require.NoError(t, err)
		require.Equal(t, "2", string(b))
	})
	t.Run("no latest", func(t *testing.T) {
		c, err := atlasexec.NewClient("", "./mock-atlas.sh")
		require.NoError(t, err)
		tt := newT(t, nil)
		tt.cli = c
		tt.setupConfigWithLogin(t, srv.URL, token)

		dir := t.TempDir()
		require.NoError(t, c.SetEnv(map[string]string{"TEST_BATCH": dir}))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "1"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "args"), []byte(fmt.Sprintf(`migrate push --dev-url sqlite://file?mode=memory --dir file://testdata/migrations --context {"path":"file://testdata/migrations","scmType":"GITHUB"} --config %s test-dir:valid-tag-123`, tt.configUrl)), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "stdout"), []byte("LINK2"), 0644))

		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("tag", "valid-tag-123")
		tt.setInput("latest", "false")
		require.NoError(t, tt.newActs(t).MigratePush(context.Background()))

		b, err := os.ReadFile(filepath.Join(dir, "counter"))
		require.NoError(t, err)
		require.Equal(t, "1", string(b))
	})
	t.Run("config", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("env", "test")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir-name", "test-dir")
		err := tt.newActs(t).MigratePush(context.Background())
		require.NoError(t, err)
	})
	t.Run("dir-name invalid characters", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-#dir")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := tt.newActs(t).MigratePush(context.Background())
		require.ErrorContains(t, err, "slug must be lowercase alphanumeric")
	})
}

func TestMigrateTest(t *testing.T) {
	t.Run("all inputs", func(t *testing.T) {
		c, err := atlasexec.NewClient("", "./mock-atlas.sh")
		require.NoError(t, err)
		require.NoError(t, c.SetEnv(map[string]string{
			"TEST_ARGS":   "migrate test --env test --config file://testdata/config/atlas.hcl --dir file://testdata/migrations --dev-url sqlite://file?mode=memory --run example --var var1=value1 --var var2=value2",
			"TEST_STDOUT": `No errors found`,
		}))
		tt := newT(t, nil)
		tt.cli = c
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("run", "example")
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		tt.setInput("vars", `{"var1": "value1", "var2": "value2"}`)
		require.NoError(t, tt.newActs(t).MigrateTest(context.Background()))
	})
}

type mockCloudClient struct {
	hash      string
	lastInput *cloud.PushSnapshotInput
}

func (m *mockCloudClient) SnapshotHash(context.Context, *cloud.SnapshotHashInput) (string, error) {
	return m.hash, nil
}

func (m *mockCloudClient) PushSnapshot(_ context.Context, i *cloud.PushSnapshotInput) (string, error) {
	m.lastInput = i
	return "url", nil
}

func TestMonitorSchema(t *testing.T) {
	const (
		u = "mysql://user:pass@host:1234/path?foo=bar"
	)
	var (
		ctx = context.Background()
	)
	for _, tt := range []struct {
		name, url, slug, config  string
		schemas, exclude         []string
		latestHash, newHash, hcl string
		exSnapshot               *cloud.SnapshotInput
		exMatch                  bool
		wantErr                  bool
	}{
		{
			name:    "no latest hash",
			url:     u,
			newHash: "hash",
			hcl:     "hcl",
			schemas: []string{},
			exclude: []string{},
			exSnapshot: &cloud.SnapshotInput{
				Hash: "hash",
				HCL:  "hcl",
			},
		},
		{
			name:       "latest hash no match",
			url:        u,
			latestHash: "different",
			newHash:    "hash",
			hcl:        "hcl",
			schemas:    []string{},
			exclude:    []string{},
			exSnapshot: &cloud.SnapshotInput{
				Hash: "hash",
				HCL:  "hcl",
			},
		},
		{
			name:       "hash match old hash func",
			url:        u,
			latestHash: atlasaction.OldAgentHash("hcl"),
			newHash:    "hash",
			hcl:        "hcl",
			schemas:    []string{},
			exclude:    []string{},
			exMatch:    true,
		},
		{
			name:       "hash match new hash func",
			url:        u,
			latestHash: "hash",
			newHash:    "hash",
			hcl:        "hcl",
			schemas:    []string{},
			exclude:    []string{},
			exMatch:    true,
		},
		{
			name:       "with slug",
			url:        u,
			slug:       "slug",
			latestHash: "hash",
			newHash:    "hash",
			hcl:        "hcl",
			schemas:    []string{},
			exclude:    []string{},
			exMatch:    true,
		},
		{
			name:       "with schema and exclude",
			url:        u,
			slug:       "slug",
			latestHash: "hash",
			newHash:    "hash",
			hcl:        "hcl",
			schemas:    []string{"foo", "bar"},
			exclude:    []string{"foo.*", "bar.*.*"},
			exMatch:    true,
		},
		{
			name:    "url and config should rerurn error",
			url:     u,
			config:  "config",
			wantErr: true,
		},
		{
			name:       "hash match old hash func, using config",
			config:     "file:/atlas.hcl",
			latestHash: atlasaction.OldAgentHash("hcl"),
			newHash:    "hash",
			hcl:        "hcl",
			schemas:    []string{},
			exclude:    []string{},
			exMatch:    true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var (
				out = &bytes.Buffer{}
				act = &mockAction{
					inputs: map[string]string{
						"cloud-token": "token",
						"url":         tt.url,
						"slug":        tt.slug,
						"config":      tt.config,
						"schemas":     strings.Join(tt.schemas, "\n"),
						"exclude":     strings.Join(tt.exclude, "\n"),
					},
					logger: slog.New(slog.NewTextHandler(out, nil)),
				}
				cli = &mockAtlas{
					login: func(ctx context.Context, params *atlasexec.LoginParams) error {
						return nil
					},
					schemaInspect: func(_ context.Context, p *atlasexec.SchemaInspectParams) (string, error) {
						return fmt.Sprintf("# %s\n# %s\n%s", tt.url, tt.newHash, tt.hcl), nil
					},
				}
				cc      = &mockCloudClient{hash: tt.latestHash}
				as, err = atlasaction.New(
					atlasaction.WithAction(act),
					atlasaction.WithAtlas(cli),
					atlasaction.WithCloudClient(func(token, version, cliVersion string) *mockCloudClient {
						return cc
					}),
				)
			)
			require.NoError(t, err)
			err = as.MonitorSchema(ctx)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, &cloud.PushSnapshotInput{
				ScopeIdent: cloud.ScopeIdent{
					URL:     tt.url,
					ExtID:   tt.slug,
					Schemas: tt.schemas,
					Exclude: tt.exclude,
				},
				Snapshot:  tt.exSnapshot,
				HashMatch: tt.exMatch,
			}, cc.lastInput)
			require.Equal(t, map[string]string{"url": "url"}, act.output)
		})
	}
}

func TestSchemaTest(t *testing.T) {
	t.Run("all inputs", func(t *testing.T) {
		c, err := atlasexec.NewClient("", "./mock-atlas.sh")
		require.NoError(t, err)
		require.NoError(t, c.SetEnv(map[string]string{
			"TEST_ARGS":   "schema test --env test --config file://testdata/config/atlas.hcl --url file://schema.hcl --dev-url sqlite://file?mode=memory --run example --var var1=value1 --var var2=value2",
			"TEST_STDOUT": `No errors found`,
		}))
		tt := newT(t, nil)
		tt.cli = c
		tt.setInput("url", "file://schema.hcl")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("run", "example")
		tt.setInput("config", "file://testdata/config/atlas.hcl")
		tt.setInput("env", "test")
		tt.setInput("vars", `{"var1": "value1", "var2": "value2"}`)
		require.NoError(t, tt.newActs(t).SchemaTest(context.Background()))
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
	tt := newT(t, nil)
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
	err = tt.newActs(t).MigratePush(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, len(payloads))
	require.Equal(t, "test-dir", payloads[0].SyncDir.Slug)
	require.Equal(t, expected, payloads[0].SyncDir.Context)
	require.Equal(t, payloads[1].PushDir.Tag, "sha1234")
	require.Equal(t, payloads[1].PushDir.Slug, "test-dir")
	tt.env["GITHUB_HEAD_REF"] = ""
	err = tt.newActs(t).MigratePush(context.Background())
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
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "required flag(s) \"dev-url\" not set")
	})
	t.Run("lint - missing dir", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir-name", "test-dir-slug")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "stat migrations: no such file or directory")
	})
	t.Run("lint - bad dir name", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "missing required parameter dir-name")
		tt.setInput("dir-name", "fake-dir-name")
		err = tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, `dir "fake-dir-name" not found`)
		tt.setInput("dir-name", "atlas://test-dir-slug") // user must not add atlas://
		err = tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, `slug must be lowercase alphanumeric and may contain /.-_`)
		out, err := tt.outputs()
		require.NoError(t, err)
		require.Equal(t, 0, len(out))
	})
	t.Run("lint summary - lint error", func(t *testing.T) {
		tt := newT(t, nil)
		var comments []map[string]any
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			// List comments endpoint
			case path == "/repos/test-owner/test-repository/issues/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			case path == "/repos/test-owner/test-repository/issues/0/comments" && method == http.MethodPost:
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
				writer.WriteHeader(http.StatusCreated)
				return
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			// Create comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodPost:
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			// Update comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/comments/123" && method == http.MethodPatch:
				require.Len(t, comments, 1)
				comments[0]["body"] = "updated comment"
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
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "<code>atlas migrate lint</code> on <strong>testdata/migrations_destructive</strong>")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "error file=testdata/migrations_destructive/20230925192914.sql")
		require.Contains(t, out, "destructive changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#DS102")
		require.Len(t, comments, 1)
		require.Equal(t, "testdata/migrations_destructive/20230925192914.sql", comments[0]["path"])
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
			"<!-- generated by ariga/atlas-action for Add a pre-migration check to ensure table \"t1\" is empty before dropping it -->", comments[0]["body"])
		require.Equal(t, float64(1), comments[0]["line"])
		// Run Lint against a directory that has an existing suggestion comment, expecting a PATCH of the comment
		err = tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Len(t, comments, 1)
		require.Equal(t, "updated comment", comments[0]["body"])
	})
	t.Run("lint summary - no text edit", func(t *testing.T) {
		tt := newT(t, nil)
		var comments []map[string]any
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
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			// Update comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/comments/123" && method == http.MethodPatch:
				require.Len(t, comments, 1)
				comments[0]["body"] = "updated comment"
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
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "<code>atlas migrate lint</code> on <strong>testdata/drop_column</strong>")
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
		tt := newT(t, nil)
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

		var comments []map[string]any
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			// List comments endpoint
			case path == "/repos/test-owner/test-repository/issues/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			case path == "/repos/test-owner/test-repository/issues/0/comments" && method == http.MethodPost:
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
				writer.WriteHeader(http.StatusCreated)
				return
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			// Create comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodPost:
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			// Update comment endpoint
			case path == "/repos/test-owner/test-repository/pulls/comments/123" && method == http.MethodPatch:
				require.Len(t, comments, 1)
				comments[0]["body"] = "updated comment"
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
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "<code>atlas migrate lint</code> on <strong>migrations_destructive</strong>")
		require.Contains(t, sum, "2 new migration files detected")
		require.Contains(t, sum, "1 reports were found in analysis")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
		out := tt.out.String()
		require.Contains(t, out, "error file=testdata/migrations_destructive/20230925192914.sql")
		require.Contains(t, out, "destructive changes detected")
		require.Contains(t, out, "Details: https://atlasgo.io/lint/analyzers#DS102")
		require.Len(t, comments, 1)
		require.Equal(t, "testdata/migrations_destructive/20230925192914.sql", comments[0]["path"])
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
			"<!-- generated by ariga/atlas-action for Add a pre-migration check to ensure table \"t1\" is empty before dropping it -->", comments[0]["body"])
		require.Equal(t, float64(1), comments[0]["line"])
		// Run Lint against a directory that has an existing suggestion comment, expecting a PATCH of the comment
		err = tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		require.Len(t, comments, 1)
		require.Equal(t, "updated comment", comments[0]["body"])
	})
	t.Run("lint summary - lint error - github api not working", func(t *testing.T) {
		tt := newT(t, nil)
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
		var comments []any
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			// List comments endpoint
			if path == "/repos/test-owner/test-repository/pulls/0/comments" && method == http.MethodGet {
				// SCM is not working
				writer.WriteHeader(http.StatusUnprocessableEntity)
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "<code>atlas migrate lint</code> on <strong>migrations_destructive</strong>")
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
		tt := newT(t, nil)
		tt.env["GITHUB_EVENT_NAME"] = "push"
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations_destructive")
		tt.setInput("dir-name", "test-dir-slug")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.ErrorContains(t, err, "`atlas migrate lint` completed with errors, see report: https://migration-lint-report-url")
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		// The summary should be create for push event
		require.NotEmpty(t, string(c))
		require.NotEmpty(t, tt.out.String())
	})
	t.Run("lint summary - with diagnostics file not included in the pull request", func(t *testing.T) {
		tt := newT(t, nil)
		var comments []map[string]any
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
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
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
		err := tt.newActs(t).MigrateLint(context.Background())
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "<code>atlas migrate lint</code> on <strong>testdata/diagnostics</strong>")
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
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.NoError(t, err)
		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		sum := string(c)
		require.Contains(t, sum, "<code>atlas migrate lint</code> on <strong>testdata/migrations</strong>")
		require.Contains(t, sum, "1 new migration file detected")
		require.Contains(t, sum, "No issues found")
		require.Contains(t, sum, `<a href="https://migration-lint-report-url" target="_blank">`)
	})
	t.Run("lint summary - lint success - vars input", func(t *testing.T) {
		tt := newT(t, nil)
		cfgURL := generateHCLWithVars(t)
		tt.setInput("config", cfgURL)
		tt.setInput("vars", fmt.Sprintf(`{"token":"%s", "url":"%s"}`, token, srv.URL))
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("dir", "file://testdata/migrations")
		tt.setInput("dir-name", "test-dir-slug")
		err := tt.newActs(t).MigrateLint(context.Background())
		require.NoError(t, err)
	})
	t.Run("lint comment", func(t *testing.T) {
		tt := newT(t, nil)
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
		err := tt.newActs(t).MigrateLint(context.Background())
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
		err = tt.newActs(t).MigrateLint(context.Background())
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
		err = tt.newActs(t).MigrateLint(context.Background())
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
		err = tt.newActs(t).MigrateLint(context.Background())
		require.Equal(t, 13, len(ghPayloads))
		require.ErrorContains(t, err, `dir "fake-dir-name" not found`)
	})
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

		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "atlas://cloud-project")
		tt.setInput("env", "test")

		// This isn't simulating a user input but is a workaround for testing Cloud SCM calls.
		cfgURL := generateHCL(t, srv.URL, "token")
		tt.setInput("config", cfgURL)
		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)

		require.Len(t, payloads, 3)
		require.Contains(t, payloads[0], "query Bot")
		require.Contains(t, payloads[1], "query dirState")
		require.Contains(t, payloads[2], "mutation ReportMigration")
		require.Contains(t, payloads[2], `"context":{"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"}`)

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n    <td>\n      <code>20230922132634</code>")
		require.Contains(t, string(c), "Migration Passed")
		require.Contains(t, string(c), "1 migration file, 1 statement passed")
	})
	t.Run("no-env", func(t *testing.T) {
		var payloads []string
		srv := httptest.NewServer(handler(&payloads))
		t.Cleanup(srv.Close)

		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "atlas://cloud-project")

		// This isn't simulating a user input but is a workaround for testing Cloud SCM calls.
		cfgURL := generateHCL(t, srv.URL, "token")
		tt.setInput("config", cfgURL)

		err := tt.newActs(t).MigrateApply(context.Background())
		require.NoError(t, err)

		require.Len(t, payloads, 2)
		require.Contains(t, payloads[0], "query Bot")
		require.Contains(t, payloads[1], "query dirState")

		c, err := os.ReadFile(tt.env["GITHUB_STEP_SUMMARY"])
		require.NoError(t, err)
		require.Contains(t, string(c), "<td>Migrate to Version</td>\n    <td>\n      <code>20230922132634</code>")
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
	cli       atlasaction.AtlasExec
	act       atlasaction.Action
	configUrl string
}

func newT(t *testing.T, w io.Writer) *test {
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
	if w == nil {
		w = &tt.out
	}
	tt.act = atlasaction.NewGHAction(func(key string) string { return tt.env[key] }, w)
	cli, err := atlasexec.NewClient("", "atlas")
	require.NoError(t, err)
	tt.cli = cli
	return tt
}

func (tt *test) newActs(t *testing.T) *atlasaction.Actions {
	t.Helper()
	c, err := atlasaction.New(
		atlasaction.WithAction(tt.act),
		atlasaction.WithAtlas(tt.cli),
		atlasaction.WithVersion("v1.2.3"),
	)
	require.NoError(t, err)
	return c
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
	tt := newT(t, nil)
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
	tt := newT(t, nil)
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

func TestSchemaPlan(t *testing.T) {
	var (
		commentCounter int
		commentEdited  int
	)
	h := http.NewServeMux()
	h.HandleFunc("GET /repos/ariga/atlas-action/issues/1/comments", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		if commentCounter == 0 {
			fmt.Fprint(w, `[]`) // No comments
		} else { // Existing comment
			fmt.Fprintf(w, `[{"id":1,"body":"<!-- generated by ariga/atlas-action for %v -->"}]`, "pr-1-Rl4lBdMk")
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
	planFile := &atlasexec.SchemaPlanFile{
		Name:     "pr-1-Rl4lBdMk",
		FromHash: "ufnTS7NrAgkvQlxbpnSxj119MAPGNqVj0i3Eelv+iLc=", // Used as comment marker
		ToHash:   "Rl4lBdMkvFoGQ4xu+3sYCeogTVnamJ7bmDoq9pMXcjw=",
		URL:      "atlas://atlas-action/plans/pr-1-Rl4lBdMk",
		Link:     "https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk",
		Status:   "PENDING",
	}
	var (
		planErr, approveErr error
		planprams           *atlasexec.SchemaPlanParams
		planFiles           []atlasexec.SchemaPlanFile
	)
	m := &mockAtlas{
		schemaPlan: func(_ context.Context, p *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error) {
			planprams = p
			// Common input checks
			require.Equal(t, "file://atlas.hcl", p.ConfigURL)
			require.Equal(t, "test", p.Env)
			require.Equal(t, "", p.Repo) // No repo, provided by atlas.hcl
			if planErr != nil {
				return nil, planErr
			}
			return &atlasexec.SchemaPlan{
				Repo: "atlas-action",
				File: planFile,
				Lint: &atlasexec.SummaryReport{Files: []*atlasexec.FileReport{}},
			}, nil
		},
		schemaPlanList: func(_ context.Context, p *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error) {
			return planFiles, nil
		},
		schemaPlanLint: func(_ context.Context, p *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error) {
			// Common input checks
			require.Equal(t, "file://atlas.hcl", p.ConfigURL)
			require.Equal(t, "test", p.Env)
			require.Equal(t, "", p.Repo) // No repo, provided by atlas.hcl
			require.Equal(t, "atlas://atlas-action/plans/pr-1-Rl4lBdMk", p.File)
			return &atlasexec.SchemaPlan{
				Repo: "atlas-action",
				File: planFile,
				Lint: &atlasexec.SummaryReport{Files: []*atlasexec.FileReport{}},
			}, nil
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
	var (
		out = &bytes.Buffer{}
		act = &mockAction{
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
			trigger: &atlasaction.TriggerContext{
				SCM:     atlasaction.SCM{Type: atlasexec.SCMTypeGithub, APIURL: srv.URL},
				Repo:    "ariga/atlas-action",
				RepoURL: "https://github.com/ariga/atlas-action",
				Branch:  "g/feature-1",
				Commit:  "commit-id",
				PullRequest: &atlasaction.PullRequest{
					Number: 1,
					URL:    "https://github.com/ariga/atlas-action/pull/1",
					Commit: "commit-id",
				},
			},
			scm: &mockSCM{baseURL: srv.URL, comments: make(map[string]struct{})},
		}
		ctx = context.Background()
	)
	// Multiple plans will fail with an error
	planFiles = []atlasexec.SchemaPlanFile{*planFile, *planFile}
	act.resetOutputs()
	act.trigger.Act = act
	newActs := func() *atlasaction.Actions {
		t.Helper()
		a, err := atlasaction.New(atlasaction.WithAction(act), atlasaction.WithAtlas(m))
		require.NoError(t, err)
		return a
	}
	require.ErrorContains(t, newActs().SchemaPlan(ctx), "found multiple schema plans, please approve or delete the existing plans")
	require.Equal(t, 0, act.summary, "No summaries generated")
	require.Equal(t, 0, commentCounter, "No more comments generated")
	require.Equal(t, 0, commentEdited, "No comment should be edited")

	// No changes
	planErr = errors.New("The current state is synced with the desired state, no changes to be made")
	planFiles = nil
	act.resetOutputs()
	require.NoError(t, newActs().SchemaPlan(ctx))
	require.Equal(t, 0, act.summary, "No summaries generated")
	require.Equal(t, 0, commentCounter, "Expected 1 comment generated")
	require.Equal(t, 0, commentEdited, "No comment should be edited")

	// No existing plan
	planErr = nil
	planFiles = nil
	act.resetOutputs()
	require.NoError(t, newActs().SchemaPlan(ctx))
	require.Equal(t, 1, act.summary, "Expected 1 summary")
	require.Equal(t, 1, commentCounter, "Expected 1 comment generated")
	require.Equal(t, 0, commentEdited, "No comment should be edited")
	require.EqualValues(t, map[string]string{
		"plan":   "atlas://atlas-action/plans/pr-1-Rl4lBdMk",
		"status": "PENDING",
		"link":   "https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk",
	}, act.output, "expected output with plan URL")

	act.trigger.PullRequest.Body = "Text\n/atlas:txmode: none\nText"
	act.resetOutputs()
	require.NoError(t, newActs().SchemaPlan(ctx))
	require.Equal(t, 2, act.summary, "Expected 2 summary")
	require.Equal(t, []string{"atlas:txmode: none"}, planprams.Directives)
	act.trigger.PullRequest.Body = ""

	// Existing plan
	planFiles = []atlasexec.SchemaPlanFile{*planFile}
	act.resetOutputs()
	require.NoError(t, newActs().SchemaPlan(ctx))
	require.Equal(t, 3, act.summary, "Expected 3 summaries")
	require.Equal(t, 1, commentCounter, "No more comments generated")
	require.Equal(t, 2, commentEdited, "Expected comment to be edited")
	require.EqualValues(t, map[string]string{
		"plan":   "atlas://atlas-action/plans/pr-1-Rl4lBdMk",
		"status": "PENDING",
		"link":   "https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk",
	}, act.output, "expected output with plan URL")

	// Check all logs output
	require.Equal(t, `time=NOW level=INFO msg="Found schema plan: atlas://atlas-action/plans/pr-1-Rl4lBdMk"
time=NOW level=INFO msg="Found schema plan: atlas://atlas-action/plans/pr-1-Rl4lBdMk"
time=NOW level=INFO msg="The current state is synced with the desired state, no changes to be made"
time=NOW level=INFO msg="Schema plan does not exist, creating a new one with name \"pr-1-ufnTS7Nr\""
time=NOW level=INFO msg="Schema plan does not exist, creating a new one with name \"pr-1-ufnTS7Nr\""
time=NOW level=INFO msg="Schema plan already exists, linting the plan \"pr-1-Rl4lBdMk\""
`, out.String())

	planFiles = nil
	act.resetOutputs()
	m.schemaPlan = func(context.Context, *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error) {
		return &atlasexec.SchemaPlan{
			File: planFile,
			Lint: &atlasexec.SummaryReport{
				Files: []*atlasexec.FileReport{{Error: "destructive changes detected"}},
			},
		}, nil
	}
	require.EqualError(t, newActs().SchemaPlan(ctx), "`atlas schema plan` completed with lint errors:\ndestructive changes detected")
}

func TestSchemaPlanApprove(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	planFile := &atlasexec.SchemaPlanFile{
		Name:     "pr-1-Rl4lBdMk",
		FromHash: "ufnTS7NrAgkvQlxbpnSxj119MAPGNqVj0i3Eelv+iLc=", // Used as comment marker
		ToHash:   "Rl4lBdMkvFoGQ4xu+3sYCeogTVnamJ7bmDoq9pMXcjw=",
		URL:      "atlas://atlas-action/plans/pr-1-Rl4lBdMk",
		Link:     "https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk",
		Status:   "PENDING",
	}
	var planFiles []atlasexec.SchemaPlanFile
	var approveErr error
	m := &mockAtlas{
		schemaPlanList: func(_ context.Context, p *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error) {
			return planFiles, nil
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
		trigger: &atlasaction.TriggerContext{
			SCM:     atlasaction.SCM{Type: atlasexec.SCMTypeGithub, APIURL: srv.URL},
			Repo:    "ariga/atlas-action",
			RepoURL: "https://github.com/ariga/atlas-action",
			Branch:  "g/feature-1",
			Commit:  "commit-id",
		},
		scm: &mockSCM{baseURL: srv.URL, comments: make(map[string]struct{})},
	}
	ctx := context.Background()
	// Multiple plans will fail with an error
	planFiles = []atlasexec.SchemaPlanFile{*planFile, *planFile}
	act.resetOutputs()
	newActs := func() *atlasaction.Actions {
		t.Helper()
		a, err := atlasaction.New(atlasaction.WithAction(act), atlasaction.WithAtlas(m))
		require.NoError(t, err)
		return a
	}
	require.ErrorContains(t, newActs().SchemaPlanApprove(ctx), "found multiple schema plans, please approve or delete the existing plans")
	require.Equal(t, 0, act.summary, "Expected 0 summary")

	// Trigger with no pull request, master branch
	planFiles = []atlasexec.SchemaPlanFile{*planFile}
	act.trigger.PullRequest = nil
	act.trigger.Branch = "master"
	act.resetOutputs()
	require.NoError(t, newActs().SchemaPlanApprove(ctx))
	require.Equal(t, 0, act.summary, "No more summaries generated")
	require.EqualValues(t, map[string]string{
		"plan":   "atlas://atlas-action/plans/pr-1-Rl4lBdMk",
		"status": "APPROVED",
		"link":   "https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk",
	}, act.output, "expected output with plan URL")

	// No pending plan
	planFiles = nil
	act.resetOutputs()
	require.NoError(t, newActs().SchemaPlanApprove(ctx))
	require.Equal(t, 0, act.summary, "No more summaries generated")
	require.EqualValues(t, map[string]string{}, act.output, "expected output with plan URL")

	// Check all logs output
	require.Equal(t, `time=NOW level=INFO msg="No plan URL provided, searching for the pending plan"
time=NOW level=INFO msg="Found schema plan: atlas://atlas-action/plans/pr-1-Rl4lBdMk"
time=NOW level=INFO msg="Found schema plan: atlas://atlas-action/plans/pr-1-Rl4lBdMk"
time=NOW level=INFO msg="No plan URL provided, searching for the pending plan"
time=NOW level=INFO msg="Schema plan approved successfully: https://gh.atlasgo.cloud/plan/pr-1-Rl4lBdMk"
time=NOW level=INFO msg="No plan URL provided, searching for the pending plan"
time=NOW level=INFO msg="No schema plan found"
`, out.String())
}

type (
	mockAction struct {
		trigger *atlasaction.TriggerContext // trigger context
		scm     *mockSCM                    // scm client
		inputs  map[string]string           // input values
		output  map[string]string           // step's output
		summary int                         // step summaries
		logger  *slog.Logger                // logger
		fatal   bool                        // fatal called
	}
	mockSCM struct {
		baseURL  string
		comments map[string]struct{}
	}
)

// MigrateApply implements atlasaction.Reporter.
func (m *mockAction) MigrateApply(context.Context, *atlasexec.MigrateApply) {
	m.summary++
}

// MigrateLint implements atlasaction.Reporter.
func (m *mockAction) MigrateLint(context.Context, *atlasexec.SummaryReport) {
	m.summary++

}

// SchemaApply implements atlasaction.Reporter.
func (m *mockAction) SchemaApply(context.Context, *atlasexec.SchemaApply) {
	m.summary++
}

// SchemaPlan implements atlasaction.Reporter.
func (m *mockAction) SchemaPlan(context.Context, *atlasexec.SchemaPlan) {
	m.summary++
}

var _ atlasaction.Action = (*mockAction)(nil)
var _ atlasaction.Reporter = (*mockAction)(nil)
var _ atlasaction.SCMClient = (*mockSCM)(nil)

func (m *mockAction) resetOutputs() {
	m.output = map[string]string{}
}

// GetType implements Action.
func (m *mockAction) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGithubAction
}

// Getenv implements Action.
func (m *mockAction) Getenv(e string) string {
	return os.Getenv(e)
}

// GetTriggerContext implements Action.
func (m *mockAction) GetTriggerContext(context.Context) (*atlasaction.TriggerContext, error) {
	return m.trigger, nil
}

// GetInput implements Action.
func (m *mockAction) GetInput(name string) string {
	return m.inputs[name]
}

// SetOutput implements Action.
func (m *mockAction) SetOutput(name, value string) {
	if m.output == nil {
		m.output = make(map[string]string)
	}
	m.output[name] = value
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

func (m *mockAction) SCM() (atlasaction.SCMClient, error) {
	return m.scm, nil
}

func (m *mockSCM) CommentLint(ctx context.Context, tc *atlasaction.TriggerContext, r *atlasexec.SummaryReport) error {
	comment, err := atlasaction.RenderTemplate("migrate-lint.tmpl", r)
	if err != nil {
		return err
	}
	return m.comment(ctx, tc.PullRequest, tc.Act.GetInput("dir-name"), comment)
}

func (m *mockSCM) CommentPlan(ctx context.Context, tc *atlasaction.TriggerContext, p *atlasexec.SchemaPlan) error {
	return m.comment(ctx, tc.PullRequest, p.File.Name, "")
}

func (m *mockSCM) comment(_ context.Context, _ *atlasaction.PullRequest, id string, _ string) error {
	var (
		method  = http.MethodPatch
		urlPath = "/repos/ariga/atlas-action/issues/comments/1"
	)
	if _, ok := m.comments[id]; !ok {
		method = http.MethodPost
		urlPath = "/repos/ariga/atlas-action/issues/1/comments"
		m.comments[id] = struct{}{}
	}
	req, err := http.NewRequest(method, m.baseURL+urlPath, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer token")
	_, err = http.DefaultClient.Do(req)
	return err
}

// Why another testscript for templates?
// Because I love to see the output in its full glory.
// Instead of a mess of quotes and escapes, I can see the actual output.
// It also allows to take output from atlas-cli and use it as input for the test.
//
// ```shell
//
//	atlas <arguments> --format "{{ json . }}"
//	cp stdout stdin
//	render template.md stdin
//	cmp stdout expected.md
//
// ```
func TestRenderTemplates(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/templates",
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"render-schema-plan":   renderTemplate[*atlasexec.SchemaPlan],
			"render-lint":          renderTemplate[*atlasexec.SummaryReport],
			"render-migrate-apply": renderTemplate[*atlasexec.MigrateApply],
		},
	})
}

func renderTemplate[T any](ts *testscript.TestScript, neg bool, args []string) {
	var data T
	if neg {
		ts.Fatalf("render commands should not fail")
	}
	if len(args) != 2 {
		ts.Fatalf("usage: render-<data> <template> <json-file>")
	}
	ts.Check(json.Unmarshal([]byte(ts.ReadFile(args[1])), &data))
	b := &bytes.Buffer{}
	ts.Check(atlasaction.CommentsTmpl.ExecuteTemplate(b, args[0], data))
	if l := b.Len(); l > 0 && b.Bytes()[l-1] != '\n' {
		ts.Check(b.WriteByte('\n')) // Add a newline the make the `cmp` command work.
	}
	_, err := io.Copy(ts.Stdout(), b)
	ts.Check(err)
}

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"atlas-action": func() int {
			return cmdapi.Main(context.Background(), "testscript", "dev")
		},
	}))
}

func TestGitHubActions(t *testing.T) {
	var (
		actions = "actions"
		output  = filepath.Join(actions, "output.txt")
		summary = filepath.Join(actions, "summary.txt")
	)
	wd, err := os.Getwd()
	require.NoError(t, err)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "github"),
		Setup: func(e *testscript.Env) (err error) {
			dir := filepath.Join(e.WorkDir, actions)
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			e.Setenv("MOCK_ATLAS", filepath.Join(wd, "mock-atlas.sh"))
			e.Setenv("GITHUB_ACTIONS", "true")
			e.Setenv("GITHUB_ENV", filepath.Join(dir, "env.txt"))
			e.Setenv("GITHUB_OUTPUT", filepath.Join(dir, "output.txt"))
			e.Setenv("GITHUB_STEP_SUMMARY", filepath.Join(dir, "summary.txt"))
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"summary": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) == 0 {
					_, err := os.Stat(ts.MkAbs(summary))
					if neg {
						if !os.IsNotExist(err) {
							ts.Fatalf("expected no summary, but got some")
						}
						return
					}
					if err != nil {
						ts.Fatalf("expected summary, but got none")
						return
					}
					return
				}
				cmpFiles(ts, neg, args[0], summary)
			},
			"output": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) == 0 {
					_, err := os.Stat(ts.MkAbs(output))
					if neg {
						if !os.IsNotExist(err) {
							ts.Fatalf("expected no output, but got some")
						}
						return
					}
					if err != nil {
						ts.Fatalf("expected output, but got none")
						return
					}
					return
				}
				cmpFiles(ts, neg, args[0], output)
			},
		},
	})
}

func cmpFiles(ts *testscript.TestScript, neg bool, name1, name2 string) {
	text1 := ts.ReadFile(name1)
	data, err := os.ReadFile(ts.MkAbs(name2))
	ts.Check(err)
	eq := text1 == string(data)
	if neg {
		if eq {
			ts.Fatalf("%s and %s do not differ", name1, name2)
		}
		return // they differ, as expected
	}
	if eq {
		return // they are equal, as expected
	}
	unifiedDiff := diff.Diff(name1, []byte(text1), name2, data)
	ts.Logf("%s", unifiedDiff)
	ts.Fatalf("%s and %s differ", name1, name2)
}
