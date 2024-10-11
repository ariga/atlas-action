package atlasaction_test

import (
	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-go-sdk/atlasexec"
	"encoding/json"
	"github.com/gorilla/mux"
	"github.com/rogpeppe/go-internal/testscript"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"testing"
)

func newMockHandler(dir string) http.Handler {
	counter := 1
	r := mux.NewRouter()
	r.Methods(http.MethodGet).Path("/projects/{project}/merge_requests/{mr}/notes").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entries, err := os.ReadDir(dir)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			comments := make([]*atlasaction.GitlabComment, len(entries))
			for i, e := range entries {
				b, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				id, err := strconv.Atoi(e.Name())
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
				comments[i] = &atlasaction.GitlabComment{
					ID:   id,
					Body: string(b),
				}
			}
			if err = json.NewEncoder(w).Encode(comments); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		})
	r.Methods(http.MethodPost).Path("/projects/{project}/merge_requests/{mr}/notes").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(counter)), []byte(body.Body+"\n"), 0666); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			counter++
			w.WriteHeader(http.StatusCreated)
			return
		})
	r.Methods(http.MethodPut).Path("/projects/{project}/merge_requests/{mr}/notes/{note}").
		HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)
			if _, err := os.Stat(path.Join(dir, vars["note"])); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			var body struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if err := os.WriteFile(filepath.Join(dir, vars["note"]), []byte(body.Body+"\n"), 0666); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		})
	return r
}

func TestGitlabCI(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "gitlab"),
		Setup: func(env *testscript.Env) error {
			commentsDir := path.Join(env.WorkDir, "comments")
			if err := os.Mkdir(commentsDir, os.ModePerm); err != nil {
				return err
			}
			srv := httptest.NewServer(newMockHandler(commentsDir))
			env.Setenv("GITLAB_CI", "true")
			env.Setenv("CI_PROJECT_ID", "1")
			env.Setenv("CI_API_V4_URL", srv.URL)
			c, err := atlasexec.NewClient(env.WorkDir, "atlas")
			if err != nil {
				return err
			}
			// Create a new actions for each test.
			env.Values[atlasKey{}] = &atlasClient{c}
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"atlas-action": atlasAction,
			"mock-atlas":   mockAtlasOutput,
			"output": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) == 0 {
					_, err := os.Stat(ts.MkAbs(".env"))
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
				cmpFiles(ts, neg, args[0], ".env")
			},
		},
	})
}
