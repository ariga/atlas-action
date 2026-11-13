package atlasaction_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-action/internal/gitlab"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

func TestGitlabCI(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	testscript.Run(t, testscript.Params{
		Dir: "testdata/gitlab",
		Setup: func(e *testscript.Env) error {
			commentsDir := filepath.Join(e.WorkDir, "comments")
			srv := httptest.NewServer(mockClientHandler(commentsDir, "token"))
			if err := os.Mkdir(commentsDir, os.ModePerm); err != nil {
				return err
			}
			e.Defer(srv.Close)
			e.Setenv("MOCK_ATLAS", filepath.Join(wd, "mock-atlas.sh"))
			e.Setenv("CI_API_V4_URL", srv.URL)
			e.Setenv("CI_PROJECT_DIR", filepath.Join(e.WorkDir, "project"))
			e.Setenv("CI_PROJECT_ID", "1")
			e.Setenv("GITLAB_CI", "true")
			e.Setenv("GITLAB_TOKEN", "token")
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"output": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) == 0 {
					_, err := os.Stat(ts.MkAbs("./project/.env"))
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
				cmpFiles(ts, neg, args[0], "./project/.env")
			},
		},
	})
}

// A later job must read each output exactly as the action wrote it, and a
// value from the database must never expand into a CI/CD variable.
func TestGitlabDotenvOutputs(t *testing.T) {
	// Variable names can be lowercase or start with "_".
	vars := map[string]string{"CI_PROJECT_ID": "88837927", "SECRET": "s3cr3t", "db_password": "pw", "_x": "x"}
	for _, tt := range []struct{ in, want string }{
		{in: ""}, {in: "true"}, {in: `"quoted"`}, {in: `{"a":"x\"y\\z"}`}, {in: "Xin chào"},
		{in: "t$CI_PROJECT_ID"}, {in: "x${CI_PROJECT_ID}y"}, {in: "x%CI_PROJECT_ID%y"},
		{in: "%SECRET%"}, {in: "%db_password%"}, {in: "$_x"}, {in: "$$SECRET"},
		{in: "$$ SELECT $1 $$"}, {in: "$body$ x $body$"}, {in: "$"}, {in: "a$"}, {in: "$$$"},
		{in: "${}"}, {in: "$${}"}, {in: "$%SECRET%"},
		{in: "%"}, {in: "%%"}, {in: "100%"}, {in: "a%20b"}, {in: "%_x%"}, {in: "%$SECRET"}, {in: "%%SECRET%%"},
		// A dotenv value cannot span lines, and GitLab reads only UTF-8.
		{in: "a\r\nb", want: `a\r\nb`}, {in: "caf\xe9", want: "caf\uFFFD"},
	} {
		if tt.want == "" {
			tt.want = tt.in
		}
		dir := t.TempDir()
		env := map[string]string{"CI_PROJECT_DIR": dir, "ATLAS_ACTION_COMMAND": "migrate/drift"}
		atlasaction.NewGitlab(func(k string) string { return env[k] }, io.Discard).SetOutput("report", tt.in)
		for _, forwarded := range []bool{false, true} {
			got := readDotenv(t, filepath.Join(dir, ".env"), vars, forwarded)
			require.Equal(t, tt.want, got["ATLAS_OUTPUT_MIGRATE_DRIFT_REPORT"], "%q, forwarded: %t", tt.in, forwarded)
		}
	}
}

var (
	// railsVarRef is Gitlab::Ci::Variables::Collection::Item::VARIABLES_REGEXP.
	railsVarRef = regexp.MustCompile(`\$\$|%%|\$([a-zA-Z_][a-zA-Z0-9_]*)|\$\{([a-zA-Z_][a-zA-Z0-9_]*)?\}|%([a-zA-Z_][a-zA-Z0-9_]*)%`)
	// rubyStrip is what Ruby's String#strip removes: ASCII whitespace and NUL.
	rubyStrip = " \t\n\v\f\r\x00"
	// forwardVarRef is ExpandVariables::VARIABLES_REGEXP, which has no escape.
	forwardVarRef = regexp.MustCompile(`\$([a-zA-Z_][a-zA-Z0-9_]*)|\$\{([a-zA-Z_][a-zA-Z0-9_]*)\}|%([a-zA-Z_][a-zA-Z0-9_]*)%`)
)

// readDotenv returns the variables a later job reads from a dotenv report,
// following GitLab's parser and the expansion passes after it:
//   - Ci::ParseDotenvArtifactService: split at the first "=", trim spaces.
//   - If forwarded to a downstream pipeline (trigger:forward:pipeline_variables),
//     ExpandVariables.expand: defined references expand, others are removed.
//   - Gitlab::Ci::Variables::Collection#expand_value with keep_undefined:
//     defined references expand, "$$" and "%%" are kept.
//   - GitLab Runner Variables.ExpandValue: os.Expand, where "$$" is "$" and
//     shell special parameters and unknown names are empty.
func readDotenv(t *testing.T, name string, vars map[string]string, forwarded bool) map[string]string {
	b, err := os.ReadFile(name)
	require.NoError(t, err)
	got := make(map[string]string)
	for _, l := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		k, v, ok := strings.Cut(l, "=")
		require.True(t, ok, l)
		v = strings.Trim(v, rubyStrip)
		if forwarded {
			v = forwardVarRef.ReplaceAllStringFunc(v, func(ref string) string {
				m := forwardVarRef.FindStringSubmatch(ref)
				return vars[m[1]+m[2]+m[3]]
			})
		}
		v = railsVarRef.ReplaceAllStringFunc(v, func(ref string) string {
			m := railsVarRef.FindStringSubmatch(ref)
			if x, ok := vars[m[1]+m[2]+m[3]]; ok {
				return x
			}
			return ref
		})
		got[strings.Trim(k, rubyStrip)] = os.Expand(v, func(k string) string {
			if k == "$" {
				return k
			}
			return vars[k]
		})
	}
	return got
}

func mockClientHandler(dir, token string) http.Handler {
	counter := 1
	m := http.NewServeMux()
	m.HandleFunc("GET /projects/{project}/merge_requests/{mr}/notes", func(w http.ResponseWriter, r *http.Request) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		comments := make([]*gitlab.Note, len(entries))
		for i, e := range entries {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			id, err := strconv.Atoi(e.Name())
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			comments[i] = &gitlab.Note{ID: id, Body: string(b)}
		}
		if err = json.NewEncoder(w).Encode(comments); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	m.HandleFunc("POST /projects/{project}/merge_requests/{mr}/notes", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(counter)), []byte(body.Body+"\n"), 0666); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		counter++
		w.WriteHeader(http.StatusCreated)
	})
	m.HandleFunc("PUT /projects/{project}/merge_requests/{mr}/notes/{note}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, r.PathValue("note"))); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, r.PathValue("note")), []byte(body.Body+"\n"), 0666); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t := r.Header.Get("PRIVATE-TOKEN"); t != token {
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		m.ServeHTTP(w, r)
	})
}
