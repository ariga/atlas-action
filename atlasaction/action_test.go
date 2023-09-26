// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ariga.io/atlas-go-sdk/atlasexec"
	"ariga.io/atlas/sql/migrate"
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

func TestMigrateApplyCloud(t *testing.T) {
	t.Run("with-dir", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir", "fail")
		tt.setInput("dir-name", "cloud-project")

		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "dir and dir-name are mutually exclusive")
	})
	t.Run("with-config", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir-name", "fail")
		tt.setInput("config", "file://atlas.hcl")

		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.ErrorContains(t, err, "config and dir-name are mutually exclusive")
	})
	t.Run("basic", func(t *testing.T) {
		tt := newT(t)
		tt.setInput("url", "sqlite://"+tt.db)
		tt.setInput("dir-name", "cloud-project")

		var payloads []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
			body := readBody(t, r.Body)
			payloads = append(payloads, body)
			switch b := body; {
			case strings.Contains(b, "query dirState"):
				dir := testDir(t, "./testdata/migrations")
				ad, err := migrate.ArchiveDir(&dir)
				require.NoError(t, err)
				// nolint:errcheck
				fmt.Fprintf(w, `{"data":{"dirState":{"content":%q}}}`, base64.StdEncoding.EncodeToString(ad))
			case strings.Contains(b, "mutation ReportMigration"):
				// nolint:errcheck
				fmt.Fprintf(w, `{"data":{"reportMigration":{"url":"https://atlas.com"}}}`)
			case strings.Contains(b, "query {\\n\\t\\t\\tme"):

			default:
				t.Log("Unhandled call: ", body)
			}
		}))
		t.Cleanup(srv.Close)
		tt.setInput("cloud-url", srv.URL)
		tt.setInput("cloud-token", "token")

		err := MigrateApply(context.Background(), tt.cli, tt.act)
		require.NoError(t, err)

		require.Len(t, payloads, 3)
		require.Contains(t, payloads[0], "query dirState")
		require.Contains(t, payloads[2], "mutation ReportMigration")

		m, err := tt.outputs()
		require.NoError(t, err)
		require.EqualValues(t, map[string]string{
			"current":       "",
			"applied_count": "1",
			"pending_count": "1",
			"target":        "20230922132634",
		}, m)
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
