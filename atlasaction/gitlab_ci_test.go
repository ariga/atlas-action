package atlasaction_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

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
