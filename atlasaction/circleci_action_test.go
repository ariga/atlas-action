// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas/sql/migrate"
	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

func Test_circleCIOrb_GetTriggerContext(t *testing.T) {
	orb := atlasaction.NewCircleCIOrb(os.Getenv, os.Stdout)
	_, err := orb.GetTriggerContext(context.Background())
	require.EqualError(t, err, "missing CIRCLE_PROJECT_REPONAME environment variable")
	t.Setenv("CIRCLE_PROJECT_REPONAME", "atlas-orb")
	_, err = orb.GetTriggerContext(context.Background())
	require.EqualError(t, err, "missing CIRCLE_SHA1 environment variable")
	t.Setenv("CIRCLE_SHA1", "1234567890")
	ctx, err := orb.GetTriggerContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, &atlasaction.TriggerContext{
		Act:    orb,
		Repo:   "atlas-orb",
		Commit: "1234567890",
	}, ctx)
	t.Setenv("GITHUB_TOKEN", "1234567890")
	t.Setenv("GITHUB_REPOSITORY", "ariga/atlas-orb")
	_, err = orb.GetTriggerContext(context.Background())
	require.EqualError(t, err, "cannot determine branch due to missing CIRCLE_BRANCH and CIRCLE_TAG environment variables")
	t.Setenv("CIRCLE_BRANCH", "main")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/ariga/atlas-orb/pulls", r.URL.Path)
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "state=open&head=ariga:main&sort=created&direction=desc&per_page=1&page=1", r.URL.RawQuery)
		_, _ = w.Write([]byte(`
		[
			{"number":1,"url":"https://api.github.com/repos/ariga/atlas-orb/pulls/9","head":{"sha":"1234567890"}},
			{"number":2,"url":"https://api.github.com/repos/ariga/atlas-orb/pulls/8","head":{"sha":"1234567890"}}
		]`))
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	ctx, err = orb.GetTriggerContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, &atlasaction.PullRequest{
		Number: 1,
		URL:    "https://api.github.com/repos/ariga/atlas-orb/pulls/9",
		Commit: "1234567890",
	}, ctx.PullRequest)
}

func TestCircleCI(t *testing.T) {
	var (
		actions = "actions"
		output  = filepath.Join(actions, "output.sh")
	)
	wd, err := os.Getwd()
	require.NoError(t, err)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "circleci"),
		Setup: func(e *testscript.Env) (err error) {
			dir := filepath.Join(e.WorkDir, actions)
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			m := http.NewServeMux()
			m.Handle("GET /repos/{owner}/{repo}/pulls", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode([]struct {
					URL    string `json:"url"`
					Number int    `json:"number"`
				}{
					{
						Number: 1,
						URL: fmt.Sprintf("https://github.com/%s/%s/pull/1",
							r.PathValue("owner"), r.PathValue("repo")),
					},
				}))
			}))
			m.Handle("GET /repos/{owner}/{repo}/issues/{num}/comments", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// No comments
				w.Write([]byte(`[]`))
			}))
			m.Handle("POST /repos/{owner}/{repo}/issues/{num}/comments", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Created comment
				w.WriteHeader(http.StatusCreated)
			}))
			m.Handle("GET /repos/{owner}/{repo}/pulls/{num}/files", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// No files
				w.Write([]byte(`[]`))
			}))
			srv := httptest.NewServer(m)
			e.Defer(srv.Close)
			e.Setenv("MOCK_ATLAS", filepath.Join(wd, "mock-atlas.sh"))
			e.Setenv("CIRCLECI", "true")
			e.Setenv("GITHUB_API_URL", srv.URL)
			e.Setenv("BASH_ENV", filepath.Join(dir, "output.sh"))
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
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
			"hashFile": func(ts *testscript.TestScript, neg bool, args []string) {
				if len(args) != 1 {
					ts.Fatalf("usage: hashFile <file>")
				}
				var hf migrate.HashFile
				if err := hf.UnmarshalText([]byte(ts.ReadFile(args[0]))); err != nil {
					ts.Fatalf("failed to unmarshal hash file: %v", err)
					return
				}
				var files []string
				for _, f := range hf {
					files = append(files, f.N)
				}
				fmt.Fprintf(ts.Stdout(), "%v", files)
			},
		},
	})
}
