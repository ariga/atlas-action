// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ariga.io/atlas-go-sdk/atlasexec"
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

		m, err := tt.outputs()
		require.NoError(t, err)
		require.EqualValues(t, map[string]string{
			"applied_count": "1",
			"current":       "",
			"pending_count": "1",
			"target":        "20230922132634",
		}, m)
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
		require.EqualError(t, err, exp)
		m, err := tt.outputs()
		require.NoError(t, err)
		require.EqualValues(t, map[string]string{
			"error": exp,
		}, m)
	})
	t.Run("baseline", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "file://testdata/migrations/")
		tt.setInput("baseline", "111_fake")
		err := MigrateApply(context.Background(), tt.cli, tt.act)
		// The error here proves that the baseline was passed to atlasexec, which
		// is what we want to test.
		exp := `atlasexec: baseline version "111_fake" not found`
		require.EqualError(t, err, exp)
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
		require.ErrorContains(t, err, `env "broken-env" not defined in project file`)
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

func TestMigrateWithCloud(t *testing.T) {
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
			Slug    string        `json:"slug"`
			Driver  string        `json:"driver"`
			Dir     string        `json:"dir"`
			Context *ContextInput `json:"context"`
		}
		graphQLQuery struct {
			Query     string          `json:"query"`
			Variables json.RawMessage `json:"variables"`
			PushDir   struct {
				pushDir `json:"input"`
			}
			SyncDir struct {
				syncDir `json:"input"`
			}
		}
	)
	var syncDirCalls, pushDirCalls int
	token := "123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
		query := graphQLQuery{}
		err := json.NewDecoder(r.Body).Decode(&query)
		require.NoError(t, err)
		switch {
		case strings.Contains(query.Query, "syncDir"):
			syncDirCalls++
			require.NoError(t, json.Unmarshal(query.Variables, &query.SyncDir))
			require.Equal(t, "test-dir", query.SyncDir.Slug)
			expected := &ContextInput{
				Repo:   "repository",
				Path:   "file://testdata/migrations",
				Branch: "testing-branch",
				Commit: "sha1234",
				URL:    "",
			}
			require.Equal(t, expected, query.SyncDir.Context)
		case strings.Contains(query.Query, "pushDir"):
			pushDirCalls++
			require.NoError(t, json.Unmarshal(query.Variables, &query.PushDir))
			require.Equal(t, query.PushDir.Tag, "sha1234")
			require.Equal(t, query.PushDir.Slug, "test-dir")
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
	tt.env["GITHUB_REF_NAME"] = "testing-branch"
	tt.env["GITHUB_SHA"] = "sha1234"
	err := MigratePush(context.Background(), tt.cli, tt.act)
	require.NoError(t, err)
	require.Equal(t, syncDirCalls, 1)
	require.Equal(t, pushDirCalls, 1)
	require.NoError(t, err)
	outputs, _ := tt.outputs()
	url := outputs["url"]
	require.Equal(t, "https://some-org.atlasgo.cloud/dirs/314159/tags/12345", url)
}

func generateHCL(t *testing.T, url, token string) string {
	tmpl := `
	atlas {
		cloud {
			token = "{{ .Token }}"
		{{- if .URL }}
			url = "{{ .URL }}"
		{{- end }}
		}	  
	}
	env "test" {
  	}
	`
	config := template.Must(template.New("atlashcl").Parse(tmpl))
	templateParams := struct {
		URL   string
		Token string
	}{
		URL:   url,
		Token: token,
	}
	var buf bytes.Buffer
	err := config.Execute(&buf, templateParams)
	require.NoError(t, err)
	atlasConfigURL, clean, err := atlasexec.TempFile(buf.String(), "hcl")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, clean())
	})
	return atlasConfigURL
}

func (tt *test) setupConfigWithLogin(t *testing.T, url, token string) {
	tt.setInput("config", generateHCL(t, url, token))
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
	act *githubactions.Action
}

func newT(t *testing.T) *test {
	outputFile, err := os.CreateTemp("", "")
	require.NoError(t, err)
	tt := &test{
		db: sqlitedb(t),
		env: map[string]string{
			"GITHUB_OUTPUT": outputFile.Name(),
		},
	}
	tt.act = githubactions.New(
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

func (t *test) setInput(k, v string) {
	t.env["INPUT_"+strings.ToUpper(k)] = v
}

// outputs is a helper that parses the GitHub Actions output file format. This is
// used to parse the output file written by the action.
func (t *test) outputs() (map[string]string, error) {
	var (
		key   string
		value strings.Builder
		token = "_GitHubActionsFileCommandDelimeter_"
	)
	m := make(map[string]string)
	c, err := os.ReadFile(t.env["GITHUB_OUTPUT"])
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
