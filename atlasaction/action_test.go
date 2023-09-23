package atlasaction

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
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
	file, err := os.CreateTemp("", "")
	require.NoError(t, err)
	db := sqlitedb(t)
	env := map[string]string{
		"INPUT_DIR":     "file://testdata/migrations/",
		"INPUT_URL":     "sqlite://" + db,
		"GITHUB_OUTPUT": file.Name(),
	}

	var b bytes.Buffer
	act := githubactions.New(
		githubactions.WithGetenv(func(key string) string {
			return env[key]
		}),
		githubactions.WithWriter(&b),
	)
	cli, err := atlasexec.NewClient("", "atlas")
	require.NoError(t, err)
	err = MigrateApply(context.Background(), cli, act)
	require.NoError(t, err)
	readFile, err := os.ReadFile(file.Name())
	require.NoError(t, err)
	m, err := readOutputFile(string(readFile))
	require.NoError(t, err)
	require.EqualValues(t, map[string]string{
		"error":         "",
		"current":       "",
		"target":        "20230922132634",
		"pending_count": "1", // Pending count before we started running.
		"applied_count": "1",
	}, m)
}

// fakeCloud returns a httptest.Server that mocks the cloud endpoint.
func fakeCloud(t *testing.T) *httptest.Server {
	dir := testDir(t, "./internal/testdata/migrations")
	ad, err := migrate.ArchiveDir(&dir)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer token", r.Header.Get("Authorization"))
		// nolint:errcheck
		fmt.Fprintf(w, `{"data":{"dir":{"content":%q}}}`, base64.StdEncoding.EncodeToString(ad))
	}))
	return srv
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

// sqlitedb returns a path to an initialized sqlite database file. The file is
// created in a temporary directory and will be deleted when the test finishes.
func sqlitedb(t *testing.T) string {
	td := t.TempDir()
	dbpath := filepath.Join(td, "file.db")
	_, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?cache=shared&_fk=1", dbpath))
	require.NoError(t, err)
	return dbpath
}

func TestParseGitHubOutputFile(t *testing.T) {
	tmp, err := os.CreateTemp("", "")
	require.NoError(t, err)
	defer os.Remove(tmp.Name())
	act := githubactions.New(
		githubactions.WithGetenv(func(key string) string {
			return map[string]string{
				"GITHUB_OUTPUT": tmp.Name(),
			}[key]
		}),
	)
	act.SetOutput("foo", "bar")
	act.SetOutput("baz", "qux")
	file, err := os.ReadFile(tmp.Name())
	require.NoError(t, err)
	m, err := readOutputFile(string(file))
	require.NoError(t, err)
	require.EqualValues(t, map[string]string{
		"foo": "bar",
		"baz": "qux",
	}, m)
}

// readOutputFile is a test helper that parses the GitHub Actions output file format. This is
// used to parse the output file written by the action.
func readOutputFile(content string) (map[string]string, error) {
	const token = "_GitHubActionsFileCommandDelimeter_"

	m := make(map[string]string)
	lines := strings.Split(content, "\n")

	var (
		key   string
		value strings.Builder
	)
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
