// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"ariga.io/atlas/sql/schema"
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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
		require.EqualError(t, err,
			"sql/migrate: executing statement \"CREATE TABLE OrderDetails (\\n    OrderDetailID INTEGER PRIMARY KEY AUTOINCREMENT,\\n    OrderID INTEGER-\\n);\" from version \"20240619073319\": near \"-\": syntax error")

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
		require.EqualError(t, err,
			"Error: sql/migrate: connected database is not clean: found multiple tables: 2. baseline version or allow-dirty is required")

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
		t.Setenv("TEST_ARGS",
			fmt.Sprintf(`migrate down --format {{ json . }} --env test --config %s --dev-url sqlite://dev?mode=memory --context {"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"} --url sqlite://%s --dir file://testdata/down/`,
				tt.configUrl, tt.db))
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
		t.Setenv("TEST_ARGS",
			fmt.Sprintf(`migrate down --format {{ json . }} --env test --config %s --dev-url sqlite://dev?mode=memory --context {"triggerType":"GITHUB_ACTION","triggerVersion":"v1.2.3"} --url sqlite://%s --dir file://testdata/down/`,
				tt.configUrl, tt.db))
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

func TestSchemaApplyWithApproval(t *testing.T) {
	setup := func(t *testing.T) *test {
		tt := newT(t, nil)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.cloud.AddSchema("example", `schema "main" {}
		table "t1" {
			schema = schema.main
			column "id" {
				type = int
				null = true
			}
		}`)
		httpMock := tt.cloud.Start(t, "token")
		tt.setupConfigWithLogin(t, httpMock.URL, "token")
		tt.cloudServer = httpMock
		return tt
	}

	t.Run("generate an approval plan on every schema changes", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ALWAYS")
		tt.setInput("to", "atlas://example")
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()), "cannot apply a migration plan in a PENDING state")
	})

	t.Run("generating an approval plan that have an existing pending plan", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ALWAYS")
		tt.setInput("to", "atlas://example")
		tt.cloud.AddPlan("pr-0-r1cgcsfo", "example", "PENDING", "R1cGcSfo1oWYK4dz+7WvgCtE/QppFo9lKFEqEDzoS4o=",
			"IILaNACeZkEfb09c0HSdi5lPLLrWf4PAo/KtDcMUxsk=")
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()), "cannot apply a migration plan in a PENDING state")
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()),
			"atlas schema plan approve --url atlas://repo/schema/example/plans/pr-0-r1cgcsfo")
	})

	t.Run("generating an approval plan when having > 1 pending plan", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ALWAYS")
		tt.setInput("to", "atlas://example")
		tt.cloud.AddPlan("pr-0-r1cgcsfo", "example", "PENDING", "R1cGcSfo1oWYK4dz+7WvgCtE/QppFo9lKFEqEDzoS4o=",
			"IILaNACeZkEfb09c0HSdi5lPLLrWf4PAo/KtDcMUxsk=")
		tt.cloud.AddPlan("pr-0-r1cgcsfo-2", "example", "PENDING", "R1cGcSfo1oWYK4dz+7WvgCtE/QppFo9lKFEqEDzoS4o=",
			"IILaNACeZkEfb09c0HSdi5lPLLrWf4PAo/KtDcMUxsk=")
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()),
			"multiple pre-planned migrations were found in the registry for this schema transition")
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()), "atlas://repo/schema/example/plans/pr-0-r1cgcsfo")
	})

	t.Run("generating an approval plan that have an existing approved plan", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ALWAYS")
		tt.setInput("to", "atlas://example")
		tt.cloud.AddPlan("pr-0-r1cgcsfo", "example", "APPROVED", "R1cGcSfo1oWYK4dz+7WvgCtE/QppFo9lKFEqEDzoS4o=",
			"IILaNACeZkEfb09c0HSdi5lPLLrWf4PAo/KtDcMUxsk=")
		require.NoError(t, tt.newActs(t).SchemaApply(context.Background()))
	})

	t.Run("generating an approval plan based on review policy with lint review = 'ALWAYS'", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ERROR")
		tt.setInput("to", "atlas://example")
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()), "enabled when review policy is set to WARNING or ERROR")
	})

	t.Run("generating an approval plan with wait-timeout", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ALWAYS")
		tt.setInput("to", "atlas://example")
		tt.setInput("wait-timeout", "3s")
		tt.setInput("wait-interval", "1s")
		tt.cloud.ApproveAllPlanAfter(t, time.Second*2)
		require.NoError(t, tt.newActs(t).SchemaApply(context.Background()))
	})

	t.Run("generating an approval plan and reaching wait timeout", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ALWAYS")
		tt.setInput("to", "atlas://example")
		tt.setInput("wait-timeout", "1s")
		require.ErrorContains(
			t,
			tt.newActs(t).SchemaApply(context.Background()),
			`was not approved within the specified waiting period. Please review the plan and re-run the action.
You can approve the plan by visiting: https://a8m.atlasgo.cloud/schemas/1/plans/1`,
		)
	})

	t.Run("generating an approval plan based on review policy with lint review = 'ERROR'", func(t *testing.T) {
		tt := setup(t)
		tt.setInput("lint-review", "ERROR")
		tt.setInput("to", "atlas://example")
		db, err := sql.Open("sqlite3", tt.db)
		require.NoError(t, err)
		_, err = db.Exec("CREATE TABLE t2 (id INTEGER PRIMARY KEY)")
		require.NoError(t, err)
		// Override the config to set the review policy to ERROR.
		config, err := url.Parse(tt.configUrl)
		require.NoError(t, err)
		file, err := os.OpenFile(config.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		require.NoError(t, err)
		file.WriteString(`
		lint {
			destructive {
				error = true
			}
		}`)
		require.ErrorContains(t, tt.newActs(t).SchemaApply(context.Background()), "cannot apply a migration plan in a PENDING state")
	})
}

type (
	mockAtlasCloud struct {
		schemas             map[string]Schema
		plans               map[string]Plan
		planFromHashes      map[string][]Plan
		planCount           int
		schemaCount         int
		approveAllPlanAfter time.Duration

		mu sync.Mutex
	}
	Schema struct {
		ID  int
		HCL string
	}
	Plan struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		SchemaSlug string `json:"schemaSlug"`
		Status     string `json:"status"`
		FromHash   string `json:"fromHash"`
		ToHash     string `json:"toHash"`
		Link       string `json:"link"`
		URL        string `json:"url"`
	}
)

func NewMockAtlasCloud() *mockAtlasCloud {
	return &mockAtlasCloud{
		schemas:        make(map[string]Schema),
		plans:          make(map[string]Plan),
		planFromHashes: make(map[string][]Plan),
	}
}

func (m *mockAtlasCloud) AddSchema(name, schema string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schemaCount++
	m.schemas[name] = Schema{
		ID:  m.schemaCount,
		HCL: schema,
	}
}

func (m *mockAtlasCloud) AddPlan(name, schemaSlug, status, fromHash, toHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planCount++
	link := fmt.Sprintf("https://a8m.atlasgo.cloud/schemas/%d/plans/%d", m.schemaCount, m.planCount)
	url := fmt.Sprintf("atlas://repo/schema/%s/plans/%s", schemaSlug, name)
	plan := Plan{
		ID:         m.planCount,
		Name:       name,
		SchemaSlug: schemaSlug,
		Status:     status,
		FromHash:   fromHash,
		ToHash:     toHash,
		Link:       link,
		URL:        url,
	}
	m.plans[name] = plan
	hash := fmt.Sprintf("%s-%s", fromHash, toHash)
	m.planFromHashes[hash] = append(m.planFromHashes[hash], plan)
}

func (m *mockAtlasCloud) ApproveAllPlanAfter(t *testing.T, after time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approveAllPlanAfter = after
}

func (m *mockAtlasCloud) Start(t *testing.T, token string) *httptest.Server {
	type (
		SchemaStateInput struct {
			Slug string `json:"slug"`
		}
		DeclarativePlanByHashesInput struct {
			SchemaSlug string `json:"schemaSlug"`
			FromHash   string `json:"fromHash"`
			ToHash     string `json:"toHash"`
		}
		DeclarativePlanByNameInput struct {
			SchemaSlug string `json:"schemaSlug"`
			Name       string `json:"name"`
		}
		CreateDeclarativePlanInput struct {
			Name       string `json:"name"`
			SchemaSlug string `json:"schemaSlug"`
			Status     string `json:"status"`
			FromHash   string `json:"fromHash"`
			ToHash     string `json:"toHash"`
		}
		graphQLQuery struct {
			Query                string          `json:"query"`
			Variables            json.RawMessage `json:"variables"`
			SchemaStateVariables struct {
				SchemaStateInput SchemaStateInput `json:"input"`
			}
			DeclarativePlanByHashesVariables struct {
				DeclarativePlanByHashesInput DeclarativePlanByHashesInput `json:"input"`
			}
			DeclarativePlanByNameVariables struct {
				DeclarativePlanByNameInput DeclarativePlanByNameInput `json:"input"`
			}
			CreateDeclarativePlanVariables struct {
				CreateDeclarativePlanInput CreateDeclarativePlanInput `json:"input"`
			}
		}
	)
	now := time.Now()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.approveAllPlanAfter > 0 && time.Since(now) > m.approveAllPlanAfter {
			for name, plan := range m.plans {
				plan.Status = "APPROVED"
				m.plans[name] = plan
			}
			for hash, plans := range m.planFromHashes {
				for i, p := range plans {
					p.Status = "APPROVED"
					m.planFromHashes[hash][i] = p
				}
			}
		}
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		var query graphQLQuery
		require.NoError(t, json.NewDecoder(r.Body).Decode(&query))
		switch {
		case strings.Contains(query.Query, "schemaState"):
			require.NoError(t, json.Unmarshal(query.Variables, &query.SchemaStateVariables))
			schema, ok := m.schemas[query.SchemaStateVariables.SchemaStateInput.Slug]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"data":{"schemaState": {"hcl": %q }}}`, schema.HCL)
		case strings.Contains(query.Query, "CreateDeclarativePlan"):
			require.NoError(t, json.Unmarshal(query.Variables, &query.CreateDeclarativePlanVariables))
			input := query.CreateDeclarativePlanVariables.CreateDeclarativePlanInput
			schema, ok := m.schemas[input.SchemaSlug]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			m.planCount++
			link := fmt.Sprintf("https://a8m.atlasgo.cloud/schemas/%d/plans/%d", schema.ID, m.planCount)
			url := fmt.Sprintf("atlas://%s/plans/%s", input.SchemaSlug, input.Name)
			plan := Plan{
				ID:         m.planCount,
				Name:       input.Name,
				SchemaSlug: input.SchemaSlug,
				Status:     input.Status,
				FromHash:   input.FromHash,
				ToHash:     input.ToHash,
				Link:       link,
				URL:        url,
			}
			m.plans[input.Name] = plan
			hash := fmt.Sprintf("%s-%s", input.FromHash, input.ToHash)
			m.planFromHashes[hash] = append(m.planFromHashes[hash], plan)
			fmt.Fprintf(w, `{"data":{"CreateDeclarativePlan": {"link": %q, "url": %q}}}`, link, url)
		case strings.Contains(query.Query, "DeclarativePlanByHashes"):
			require.NoError(t, json.Unmarshal(query.Variables, &query.DeclarativePlanByHashesVariables))
			input := query.DeclarativePlanByHashesVariables.DeclarativePlanByHashesInput
			hash := fmt.Sprintf("%s-%s", input.FromHash, input.ToHash)
			plan, ok := m.planFromHashes[hash]
			if !ok {
				fmt.Fprintf(w, `{"data":{"DeclarativePlanByHashes": []}}`)
				return
			}
			fmt.Fprintf(w, `{"data":{"DeclarativePlanByHashes": %s}}`, must(json.Marshal(plan)))
		case strings.Contains(query.Query, "DeclarativePlanByName"):
			require.NoError(t, json.Unmarshal(query.Variables, &query.DeclarativePlanByNameVariables))
			input := query.DeclarativePlanByNameVariables.DeclarativePlanByNameInput
			plan, ok := m.plans[input.Name]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"data":{"DeclarativePlanByName": %s}}`, must(json.Marshal(plan)))
		}
	}))

}

type mockAtlas struct {
	login             func(context.Context, *atlasexec.LoginParams) error
	migrateDiff       func(context.Context, *atlasexec.MigrateDiffParams) (*atlasexec.MigrateDiff, error)
	migrateDown       func(context.Context, *atlasexec.MigrateDownParams) (*atlasexec.MigrateDown, error)
	migrateHash       func(context.Context, *atlasexec.MigrateHashParams) error
	migrateRebase     func(context.Context, *atlasexec.MigrateRebaseParams) error
	schemaInspect     func(context.Context, *atlasexec.SchemaInspectParams) (string, error)
	schemaPush        func(context.Context, *atlasexec.SchemaPushParams) (*atlasexec.SchemaPush, error)
	schemaPlan        func(context.Context, *atlasexec.SchemaPlanParams) (*atlasexec.SchemaPlan, error)
	schemaPlanList    func(context.Context, *atlasexec.SchemaPlanListParams) ([]atlasexec.SchemaPlanFile, error)
	schemaPlanLint    func(context.Context, *atlasexec.SchemaPlanLintParams) (*atlasexec.SchemaPlan, error)
	schemaPlanApprove func(context.Context, *atlasexec.SchemaPlanApproveParams) (*atlasexec.SchemaPlanApprove, error)
	whoAmI            func(context.Context, *atlasexec.WhoAmIParams) (*atlasexec.WhoAmI, error)
	schemaLint        func(context.Context, *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error)
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

// MigrateDiff implements AtlasExec.
func (m *mockAtlas) MigrateDiff(ctx context.Context, params *atlasexec.MigrateDiffParams) (*atlasexec.MigrateDiff, error) {
	return m.migrateDiff(ctx, params)
}

// MigrateStatus implements AtlasExec.
func (m *mockAtlas) MigrateStatus(context.Context, *atlasexec.MigrateStatusParams) (*atlasexec.MigrateStatus, error) {
	panic("unimplemented")
}

// MigrateHash implements AtlasExec.
func (m *mockAtlas) MigrateHash(ctx context.Context, params *atlasexec.MigrateHashParams) error {
	return m.migrateHash(ctx, params)
}

// MigrateInspect implements AtlasExec.
func (m *mockAtlas) MigrateRebase(ctx context.Context, params *atlasexec.MigrateRebaseParams) error {
	return m.migrateRebase(ctx, params)
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

// WhoAmI implements AtlasExec.
func (m *mockAtlas) WhoAmI(ctx context.Context, params *atlasexec.WhoAmIParams) (*atlasexec.WhoAmI, error) {
	return m.whoAmI(ctx, params)
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
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "args"),
			[]byte(fmt.Sprintf(`migrate push --dev-url sqlite://file?mode=memory --dir file://testdata/migrations --context {"path":"file://testdata/migrations","scmType":"GITHUB"} --config %s test-dir`,
				tt.configUrl)), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "stdout"), []byte("LINK1"), 0644))
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "2"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "2", "args"),
			[]byte(fmt.Sprintf(`migrate push --dev-url sqlite://file?mode=memory --dir file://testdata/migrations --context {"path":"file://testdata/migrations","scmType":"GITHUB"} --config %s test-dir:valid-tag-123`,
				tt.configUrl)), 0644))
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
		require.NoError(t, os.WriteFile(filepath.Join(dir, "1", "args"),
			[]byte(fmt.Sprintf(`migrate push --dev-url sqlite://file?mode=memory --dir file://testdata/migrations --context {"path":"file://testdata/migrations","scmType":"GITHUB"} --config %s test-dir:valid-tag-123`,
				tt.configUrl)), 0644))
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

type MockCmdExecutor struct {
	ran []struct {
		name string
		args []string
	}
	onCommand func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// ExecCmd is the mocked function compatible with your CmdExecutor.
func (m *MockCmdExecutor) ExecCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	m.ran = append(m.ran, struct {
		name string
		args []string
	}{name: name, args: args})
	return m.onCommand(ctx, name, args...)
}

func TestMigrateAutoRebase(t *testing.T) {
	t.Run("no conflict", func(t *testing.T) {
		c, err := atlasexec.NewClient("", "atlas")
		require.NoError(t, err)
		out := &bytes.Buffer{}
		mockExec := &MockCmdExecutor{
			onCommand: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				// Simulate result when running: git show
				if len(args) > 1 && args[0] == "show" {
					res := `h1:I/42uUoInXTRcwooAuTKQpGPF4jfNmEqDD1L66btb+E=
                    		20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
                    		20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=`
					// print the result to the stdout
					return exec.CommandContext(ctx, "echo", res)
				}
				// Dummy command to avoid errors
				return exec.CommandContext(ctx, "echo")
			},
		}
		act := &mockAction{
			inputs: map[string]string{
				"dir": "file://testdata/migrations",
			},
			trigger: &atlasaction.TriggerContext{
				Branch:        "my-branch",
				DefaultBranch: "main",
			},
			logger: slog.New(slog.NewTextHandler(out, nil)),
		}
		acts, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithAtlas(c),
			atlasaction.WithCmdExecutor(mockExec.ExecCmd),
		)
		require.NoError(t, err)

		require.NoError(t, acts.MigrateAutoRebase(context.Background()))
		require.Contains(t, out.String(), "No new migration files to rebase")
		// Check that the correct git commands were executed
		require.Len(t, mockExec.ran, 5)
		require.Equal(t, []string{"--version"}, mockExec.ran[0].args)
		require.Equal(t, []string{"fetch", "origin", "main"}, mockExec.ran[1].args)
		require.Equal(t, []string{"checkout", "my-branch"}, mockExec.ran[2].args)
		require.Equal(t, []string{"show", "origin/main:testdata/migrations/atlas.sum"}, mockExec.ran[3].args)
		require.Equal(t, []string{"show", "origin/my-branch:testdata/migrations/atlas.sum"}, mockExec.ran[4].args)
	})
	t.Run("conflict in atlas.sum", func(t *testing.T) {
		var rebasedFiles []string
		cli := &mockAtlas{
			migrateHash: func(ctx context.Context, p *atlasexec.MigrateHashParams) error {
				return nil
			},
			migrateRebase: func(ctx context.Context, params *atlasexec.MigrateRebaseParams) error {
				rebasedFiles = params.Files
				return nil
			},
		}
		out := &bytes.Buffer{}
		mockExec := &MockCmdExecutor{
			onCommand: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				// Dummy command to avoid errors
				cmd := exec.CommandContext(ctx, "echo")
				switch {
				// Simulate a conflict when running `git merge --no-ff origin/rebase-branch`
				case len(args) > 2 && args[0] == "merge" && args[2] == "origin/rebase-branch":
					cmd.Err = &exec.ExitError{Stderr: []byte("conflict")}
				// Simulate result when running: git show
				case len(args) > 1 && args[0] == "show":
					var res string
					switch args[1] {
					case "origin/rebase-branch:testdata/need_rebase/atlas.sum":
						res = `h1:I/42uUoInXTRcwooAuTKQpGPF4jfNmEqDD1L66btb+E=
                               20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
                               20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=`
					case "origin/my-branch:testdata/need_rebase/atlas.sum":
						res = `h1:U12LVflnyphPTk0O6cKIbrbaea0L3nJx+DJ8nRVuvn8=
                               20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
                               20250309093464_rebase.sql h1:H7yD0qrDOB7HQvUUkyrX2N4qspo6/Mro+Od+l8XCX+c=`
					}
					// print the result to the stdout
					cmd = exec.CommandContext(ctx, "echo", res)
				// Simulate result when running: git diff --name-only
				case len(args) > 1 && args[0] == "diff" && args[1] == "--name-only":
					cmd = exec.CommandContext(ctx, "echo", "testdata/need_rebase/atlas.sum")
				}
				return cmd
			},
		}
		act := &mockAction{
			inputs: map[string]string{
				"dir":         "file://testdata/need_rebase",
				"base-branch": "rebase-branch",
			},
			trigger: &atlasaction.TriggerContext{
				Branch: "my-branch",
			},
			logger: slog.New(slog.NewTextHandler(out, nil)),
		}
		acts, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithAtlas(cli),
			atlasaction.WithCmdExecutor(mockExec.ExecCmd),
		)
		require.NoError(t, err)

		require.NoError(t, acts.MigrateAutoRebase(context.Background()))
		require.Contains(t, out.String(), "Migrations rebased successfully")
		// Check files were rebased
		require.Len(t, rebasedFiles, 1)
		require.Equal(t, "20250309093464_rebase.sql", rebasedFiles[0])
		// Check that the correct git commands were executed
		require.Len(t, mockExec.ran, 10)
		require.Equal(t, []string{"--version"}, mockExec.ran[0].args)
		require.Equal(t, []string{"fetch", "origin", "rebase-branch"}, mockExec.ran[1].args)
		require.Equal(t, []string{"checkout", "my-branch"}, mockExec.ran[2].args)
		require.Equal(t, []string{"show", "origin/rebase-branch:testdata/need_rebase/atlas.sum"}, mockExec.ran[3].args)
		require.Equal(t, []string{"show", "origin/my-branch:testdata/need_rebase/atlas.sum"}, mockExec.ran[4].args)
		require.Equal(t, []string{"merge", "--no-ff", "origin/rebase-branch"}, mockExec.ran[5].args)
		require.Equal(t, []string{"diff", "--name-only", "--diff-filter=U"}, mockExec.ran[6].args)
		require.Equal(t, []string{"add", "testdata/need_rebase"}, mockExec.ran[7].args)
		require.Equal(t, []string{"commit", "--message", "testdata/need_rebase: rebase migration files"}, mockExec.ran[8].args)
		require.Equal(t, []string{"push", "origin", "my-branch"}, mockExec.ran[9].args)
	})
	t.Run("conflict, but not only in atlas.sum", func(t *testing.T) {
		mockExec := &MockCmdExecutor{
			onCommand: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				// Dummy command to avoid errors
				cmd := exec.CommandContext(ctx, "echo")
				switch {
				// Simulate a conflict when running `git merge --no-ff origin/rebase-branch`
				case len(args) > 2 && args[0] == "merge" && args[2] == "origin/rebase-branch":
					cmd.Err = &exec.ExitError{Stderr: []byte("conflict")}
				// Simulate result when running: git show
				case len(args) > 1 && args[0] == "show":
					var res string
					switch args[1] {
					case "origin/rebase-branch:testdata/need_rebase/atlas.sum":
						res = `h1:I/42uUoInXTRcwooAuTKQpGPF4jfNmEqDD1L66btb+E=
                               20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
                               20250309093833_second.sql h1:gDi08EnaiS7cPo+IbS72CkQFg/2vanxGLMjfNN9XHEE=`
					case "origin/my-branch:testdata/need_rebase/atlas.sum":
						res = `h1:U12LVflnyphPTk0O6cKIbrbaea0L3nJx+DJ8nRVuvn8=
                               20250309093454_init_1.sql h1:h6tXkQgcuEtcMlIT3Q2ei1WKXqaqb2PK7F87YFUcSR4=
                               20250309093464_rebase.sql h1:H7yD0qrDOB7HQvUUkyrX2N4qspo6/Mro+Od+l8XCX+c=`
					}
					// print the result to the stdout
					cmd = exec.CommandContext(ctx, "echo", res)
				// Simulate result when running: git diff --name-only
				case len(args) > 1 && args[0] == "diff" && args[1] == "--name-only":
					cmd = exec.CommandContext(ctx, "echo", "testdata/need_rebase/atlas.sum\n not_atlas.sum")
				}
				return cmd
			},
		}
		act := &mockAction{
			inputs: map[string]string{
				"dir":         "file://testdata/need_rebase",
				"base-branch": "rebase-branch",
			},
			trigger: &atlasaction.TriggerContext{
				Branch: "my-branch",
			},
			logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		}
		acts, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithCmdExecutor(mockExec.ExecCmd),
		)
		require.NoError(t, err)

		err = acts.MigrateAutoRebase(context.Background())
		require.EqualError(t, err, "conflict found in files other than testdata/need_rebase/atlas.sum")
		// Check that the correct git commands were executed
		require.Len(t, mockExec.ran, 7)
		require.Equal(t, []string{"--version"}, mockExec.ran[0].args)
		require.Equal(t, []string{"fetch", "origin", "rebase-branch"}, mockExec.ran[1].args)
		require.Equal(t, []string{"checkout", "my-branch"}, mockExec.ran[2].args)
		require.Equal(t, []string{"show", "origin/rebase-branch:testdata/need_rebase/atlas.sum"}, mockExec.ran[3].args)
		require.Equal(t, []string{"show", "origin/my-branch:testdata/need_rebase/atlas.sum"}, mockExec.ran[4].args)
		require.Equal(t, []string{"merge", "--no-ff", "origin/rebase-branch"}, mockExec.ran[5].args)
		require.Equal(t, []string{"diff", "--name-only", "--diff-filter=U"}, mockExec.ran[6].args)
	})
}

func TestMigrateDiff(t *testing.T) {
	t.Run("no diff", func(t *testing.T) {
		c, err := atlasexec.NewClient("", "atlas")
		require.NoError(t, err)
		out := &bytes.Buffer{}
		act := &mockAction{
			inputs: map[string]string{
				"to":      "file://testdata/migrations",
				"dir":     "file://testdata/migrations",
				"dev-url": "sqlite://file?mode=memory",
			},
			trigger: &atlasaction.TriggerContext{
				Branch: "my-branch",
			},
			logger: slog.New(slog.NewTextHandler(out, nil)),
		}
		acts, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithAtlas(c),
		)
		require.NoError(t, err)

		require.NoError(t, acts.MigrateDiff(context.Background()))
		require.Contains(t, out.String(), "The migration directory is synced with the desired state")
	})
	t.Run("there is diff", func(t *testing.T) {
		cli := &mockAtlas{
			migrateDiff: func(ctx context.Context, p *atlasexec.MigrateDiffParams) (*atlasexec.MigrateDiff, error) {
				return &atlasexec.MigrateDiff{
					Files: []atlasexec.File{
						{Content: "create table t1 ( c int );", Name: "t1.sql"},
					},
					Dir: "file://testdata/migrations",
				}, nil
			},
			migrateHash: func(ctx context.Context, p *atlasexec.MigrateHashParams) error {
				return nil
			},
		}
		var (
			out = &bytes.Buffer{}
			td  = t.TempDir()
		)
		mockExec := &MockCmdExecutor{
			onCommand: func(ctx context.Context, name string, args ...string) *exec.Cmd {
				// Dummy command to avoid errors
				return exec.CommandContext(ctx, "echo")
			},
		}
		act := &mockAction{
			inputs: map[string]string{
				"to":      fmt.Sprintf("file://%s/schema.sql", td),
				"dir":     "file://testdata/migrations",
				"dev-url": "sqlite://file?mode=memory",
			},
			trigger: &atlasaction.TriggerContext{
				Branch:        "my-branch",
				DefaultBranch: "main",
			},
			logger: slog.New(slog.NewTextHandler(out, nil)),
		}
		acts, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithAtlas(cli),
			atlasaction.WithCmdExecutor(mockExec.ExecCmd),
		)
		require.NoError(t, err)

		require.NoError(t, acts.MigrateDiff(context.Background()))
		require.Contains(t, out.String(), "Run migrate/diff completed successfully")
		// Check that the correct commands were executed
		require.Len(t, mockExec.ran, 5)
		require.Equal(t, []string{"--version"}, mockExec.ran[0].args)
		require.Equal(t, []string{"checkout", "my-branch"}, mockExec.ran[1].args)
		require.Equal(t, []string{"add", "testdata/migrations"}, mockExec.ran[2].args)
		require.Equal(t, []string{"commit", "--message", "testdata/migrations: add new migration file"}, mockExec.ran[3].args)
		require.Equal(t, []string{"push", "origin", "my-branch"}, mockExec.ran[4].args)
		// Ensure migration file was created
		require.FileExists(t, filepath.Join("testdata/migrations", "t1.sql"))
		t.Cleanup(func() {
			_ = os.Remove(filepath.Join("testdata/migrations", "t1.sql"))
		})
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
			resp.Data.Dirs = []Dir{
				{
					Name:    "test-dir-name",
					Slug:    "test-dir-slug",
					Content: base64.StdEncoding.EncodeToString(ad),
				}, {
					Name:    "other-dir-name",
					Slug:    "other-dir-slug",
					Content: base64.StdEncoding.EncodeToString(ad),
				},
			}
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
			"<!-- generated by ariga/atlas-action for Add a pre-migration check to ensure table \"t1\" is empty before dropping it -->",
			comments[0]["body"])
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
				resp.Data.Dirs = []Dir{
					{
						Name:    "test-dir-name",
						Slug:    "test-dir-slug",
						Content: base64.StdEncoding.EncodeToString(ad),
					}, {
						Name:    "other-dir-name",
						Slug:    "other-dir-slug",
						Content: base64.StdEncoding.EncodeToString(ad),
					},
				}
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
			"<!-- generated by ariga/atlas-action for Add a pre-migration check to ensure table \"t1\" is empty before dropping it -->",
			comments[0]["body"])
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
				resp.Data.Dirs = []Dir{
					{
						Name:    "test-dir-name",
						Slug:    "test-dir-slug",
						Content: base64.StdEncoding.EncodeToString(ad),
					}, {
						Name:    "other-dir-name",
						Slug:    "other-dir-slug",
						Content: base64.StdEncoding.EncodeToString(ad),
					},
				}
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
	// Mock Atlas Cloud
	cloud *mockAtlasCloud
	// Initialized in the test setup
	cloudServer *httptest.Server
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
	tt.cloud = NewMockAtlasCloud()
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

func (m *mockSCM) CommentSchemaLint(ctx context.Context, tc *atlasaction.TriggerContext, r *atlasaction.SchemaLintReport) error {
	comment, err := atlasaction.RenderTemplate("schema-lint.tmpl", r)
	if err != nil {
		return err
	}
	return m.comment(ctx, tc.PullRequest, "schema-lint", comment)
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
			"render-schema-lint":   renderTemplate[*atlasaction.SchemaLintReport],
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

// SchemaLint implements AtlasExec.
func (m *mockAtlas) SchemaLint(ctx context.Context, p *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error) {
	return m.schemaLint(ctx, p)
}

// SchemaLint implements atlasaction.Reporter.
func (m *mockAction) SchemaLint(context.Context, *atlasaction.SchemaLintReport) {
	m.summary++
}

func TestSchemaLint(t *testing.T) {
	token := "123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
	}))
	t.Run("lint - missing dev-url", func(t *testing.T) {
		tt := newT(t, nil)
		tt.setupConfigWithLogin(t, srv.URL, token)
		tt.setInput("url", "file://schema.hcl")
		err := tt.newActs(t).SchemaLint(context.Background())
		require.ErrorContains(t, err, "dev-url")
	})
	t.Run("lint - success", func(t *testing.T) {
		m := &mockAtlas{}
		m.schemaLint = func(_ context.Context, p *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error) {
			require.Equal(t, "test", p.Env)
			require.Equal(t, "file://testdata/config/atlas.hcl", p.ConfigURL)
			require.Equal(t, "sqlite://file?mode=memory", p.DevURL)
			require.Equal(t, []string{"file://schema.hcl"}, p.URL)
			require.Equal(t, []string{"users", "posts"}, p.Schema)
			return &atlasexec.SchemaLintReport{
				Steps: []atlasexec.Report{},
			}, nil
		}
		act := &mockAction{
			inputs: map[string]string{
				"url":     "file://schema.hcl",
				"dev-url": "sqlite://file?mode=memory",
				"schema":  "users\nposts",
				"config":  "file://testdata/config/atlas.hcl",
				"env":     "test",
				"vars":    `{"var1": "value1", "var2": "value2"}`,
			},
			output: map[string]string{},
			trigger: &atlasaction.TriggerContext{
				PullRequest: &atlasaction.PullRequest{
					URL: "http://test",
				},
				SCM: atlasaction.SCM{Type: "NONE"},
			},
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		a, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithAtlas(m),
		)
		require.NoError(t, err)
		err = a.SchemaLint(context.Background())
		require.NoError(t, err)
		require.Equal(t, 0, act.summary)
	})
	t.Run("lint - with issues", func(t *testing.T) {
		m := &mockAtlas{}
		m.schemaLint = func(_ context.Context, p *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error) {
			require.Equal(t, "test", p.Env)
			require.Equal(t, "file://testdata/config/atlas.hcl", p.ConfigURL)
			require.Equal(t, "sqlite://file?mode=memory", p.DevURL)
			require.Equal(t, []string{"file://schema.hcl"}, p.URL)
			return &atlasexec.SchemaLintReport{
				Steps: []atlasexec.Report{
					{
						Text: "Issue found",
						Diagnostics: []atlasexec.Diagnostic{
							{
								Text: "Issue detail",
								Code: "LINT001",
							},
						},
					},
				},
			}, nil
		}
		act := &mockAction{
			inputs: map[string]string{
				"url":     "file://schema.hcl",
				"dev-url": "sqlite://file?mode=memory",
				"config":  "file://testdata/config/atlas.hcl",
				"env":     "test",
			},
			output: map[string]string{},
			trigger: &atlasaction.TriggerContext{
				PullRequest: &atlasaction.PullRequest{
					URL: "http://test",
				},
				SCM: atlasaction.SCM{Type: "NONE"},
			},
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
		a, err := atlasaction.New(
			atlasaction.WithAction(act),
			atlasaction.WithAtlas(m),
		)
		require.NoError(t, err)
		err = a.SchemaLint(context.Background())
		require.Nil(t, err)
		require.Equal(t, 1, act.summary)
	})
	t.Run("lint - PR comment", func(t *testing.T) {
		tt := newT(t, nil)
		var comments []map[string]any
		reviewCommentID := 1
		reviewComments := map[int]map[string]any{
			reviewCommentID: {
				"id":   reviewCommentID,
				"body": "first comment",
			},
		}
		ghMock := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			var (
				path   = request.URL.Path
				method = request.Method
			)
			switch {
			case path == "/repos/test-owner/test-repository/issues/42/comments" && method == http.MethodGet:
				b, err := json.Marshal(comments)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			case path == "/repos/test-owner/test-repository/issues/42/comments" && method == http.MethodPost:
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				payload["id"] = 123
				comments = append(comments, payload)
				writer.WriteHeader(http.StatusCreated)
				return
			case path == "/repos/test-owner/test-repository/pulls/42/files" && method == http.MethodGet:
				_, err := writer.Write([]byte(`[{"filename": "path/to/file1.go"}, {"filename": "path/to/file2.go"}]`))
				require.NoError(t, err)
				return
			case path == "/repos/test-owner/test-repository/pulls/42/comments" && method == http.MethodGet:
				var payload []map[string]any
				for _, v := range reviewComments {
					payload = append(payload, v)
				}
				b, err := json.Marshal(payload)
				require.NoError(t, err)
				_, err = writer.Write(b)
				require.NoError(t, err)
				return
			case path == "/repos/test-owner/test-repository/pulls/42/comments" && method == http.MethodPost:
				var payload map[string]any
				require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
				reviewCommentID++
				payload["id"] = reviewCommentID
				reviewComments[reviewCommentID] = payload
				writer.WriteHeader(http.StatusCreated)
				return
			case strings.HasPrefix(path, "/repos/test-owner/test-repository/pulls/comments/") && method == http.MethodDelete:
				idStr := strings.TrimPrefix(path, "/repos/test-owner/test-repository/pulls/comments/")
				if id, err := strconv.Atoi(idStr); err == nil {
					delete(reviewComments, id)
				}
				writer.WriteHeader(http.StatusNoContent)
				return
			}
		}))
		tt.env["GITHUB_API_URL"] = ghMock.URL
		tt.env["GITHUB_REPOSITORY"] = "test-owner/test-repository"
		tt.setEvent(t, `{
			"pull_request": {
				"number": 42
			}
		}`)
		mockAtlas := &mockAtlas{}
		mockAtlas.schemaLint = func(_ context.Context, p *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error) {
			return &atlasexec.SchemaLintReport{
				Steps: []atlasexec.Report{
					{
						Text: "naming conventions",
						Diagnostics: []atlasexec.Diagnostic{
							{
								Text: "Schema name violates the naming convention",
								Code: "DS102",
							},
							{
								Text: "Table name violates the naming convention",
								Code: "DS103",
							},
						},
					},
					{
						Text: "rule \"primary-key-required\"",
						Desc: "All tables must have a primary key",
						Diagnostics: []atlasexec.Diagnostic{
							{
								Text: "Table t1 must have a primary key",
							},
							{
								Text: "Table t2 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file1.go",
								},
							},
							{
								Text: "Table t3 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file1.go",
									Start:    struct{ Line, Column, Byte int }{Line: 10},
								},
							},
							{
								Text: "Table t4 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file2.go",
									Start:    struct{ Line, Column, Byte int }{Line: 10},
									End:      struct{ Line, Column, Byte int }{Line: 20},
								},
							},
							{
								Text: "Table t5 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file3.go",
									Start:    struct{ Line, Column, Byte int }{Line: 10},
									End:      struct{ Line, Column, Byte int }{Line: 20},
								},
							},
						},
					},
				},
			}, nil
		}
		tt.env["GITHUB_TOKEN"] = "very-secret-gh-token"
		tt.setInput("dev-url", "sqlite://file?mode=memory")
		tt.setInput("url", "file://schema.hcl")
		tt.setInput("schema-name", "test-schema")
		a := tt.newActs(t)
		a.Atlas = mockAtlas
		err := a.SchemaLint(context.Background())
		require.Nil(t, err)
		require.Len(t, comments, 1)
		require.Contains(t, comments[0]["body"].(string), "Naming conventions")
		require.Contains(t, comments[0]["body"].(string), "Rule \"primary-key-required\"")
		require.Len(t, reviewComments, 3)
		require.Equal(t, map[string]any{
			"id":   1,
			"body": "first comment",
		}, reviewComments[1])
		require.Equal(t, map[string]any{
			"id":         2,
			"body":       "Table t3 must have a primary key\n<!-- generated by ariga/atlas-action for schema-lint:path/to/file1.go:10-0:Table t3 must have a primary key -->",
			"path":       "path/to/file1.go",
			"start_line": float64(10),
			"line":       float64(10),
		}, reviewComments[2])
		require.Equal(t, map[string]any{
			"id":         3,
			"body":       "Table t4 must have a primary key\n<!-- generated by ariga/atlas-action for schema-lint:path/to/file2.go:10-20:Table t4 must have a primary key -->",
			"path":       "path/to/file2.go",
			"start_line": float64(10),
			"line":       float64(20),
		}, reviewComments[3])
		// Rerun schema lint
		mockAtlas.schemaLint = func(_ context.Context, p *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error) {
			return &atlasexec.SchemaLintReport{
				Steps: []atlasexec.Report{
					{
						Text: "naming conventions",
						Diagnostics: []atlasexec.Diagnostic{
							{
								Text: "Schema name violates the naming convention",
								Code: "DS102",
							},
							{
								Text: "Table name violates the naming convention",
								Code: "DS103",
							},
						},
					},
					{
						Text: "rule \"primary-key-required\"",
						Desc: "All tables must have a primary key",
						Diagnostics: []atlasexec.Diagnostic{
							{
								Text: "Table t1 must have a primary key",
							},
							{
								Text: "Table t2 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file1.go",
								},
							},
							{
								Text: "Table t4 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file2.go",
									Start:    struct{ Line, Column, Byte int }{Line: 10},
									End:      struct{ Line, Column, Byte int }{Line: 20},
								},
							},
							{
								Text: "Table t5 must have a primary key",
								Pos: &schema.Pos{
									Filename: "path/to/file2.go",
									Start:    struct{ Line, Column, Byte int }{Line: 30},
									End:      struct{ Line, Column, Byte int }{Line: 40},
								},
							},
						},
					},
				},
			}, nil
		}
		err = a.SchemaLint(context.Background())
		require.Nil(t, err)
		require.Len(t, comments, 1)
		require.Contains(t, comments[0]["body"].(string), "Naming conventions")
		require.Contains(t, comments[0]["body"].(string), "Rule \"primary-key-required\"")
		require.Len(t, reviewComments, 3)
		require.Equal(t, map[string]any{
			"id":   1,
			"body": "first comment",
		}, reviewComments[1])
		require.Equal(t, map[string]any{
			"id":         3,
			"body":       "Table t4 must have a primary key\n<!-- generated by ariga/atlas-action for schema-lint:path/to/file2.go:10-20:Table t4 must have a primary key -->",
			"path":       "path/to/file2.go",
			"start_line": float64(10),
			"line":       float64(20),
		}, reviewComments[3])
		require.Equal(t, map[string]any{
			"id":         4,
			"body":       "Table t5 must have a primary key\n<!-- generated by ariga/atlas-action for schema-lint:path/to/file2.go:30-40:Table t5 must have a primary key -->",
			"path":       "path/to/file2.go",
			"start_line": float64(30),
			"line":       float64(40),
		}, reviewComments[4])
		// Schema lint has no steps, return Success
		comments = []map[string]any{}
		reviewComments = map[int]map[string]any{}
		mockAtlas.schemaLint = func(_ context.Context, p *atlasexec.SchemaLintParams) (*atlasexec.SchemaLintReport, error) {
			return &atlasexec.SchemaLintReport{
				Steps: []atlasexec.Report{},
			}, nil
		}
		err = a.SchemaLint(context.Background())
		require.NoError(t, err)
		require.Len(t, comments, 0)
		require.Len(t, reviewComments, 0)
	})
}
