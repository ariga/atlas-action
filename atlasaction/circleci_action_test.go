// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-go-sdk/atlasexec"
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
		output  = filepath.Join(actions, "output.txt")
	)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "circleci"),
		Setup: func(e *testscript.Env) (err error) {
			dir := filepath.Join(e.WorkDir, actions)
			if err := os.Mkdir(dir, 0700); err != nil {
				return err
			}
			e.Setenv("CIRCLECI", "true")
			e.Setenv("CIRCLE_PROJECT_REPONAME", "atlas-orb")
			e.Setenv("CIRCLE_SHA1", "1234567890")
			e.Setenv("BASH_ENV", filepath.Join(dir, "output.txt"))
			c, err := atlasexec.NewClient(e.WorkDir, "atlas")
			if err != nil {
				return err
			}
			// Create a new actions for each test.
			e.Values[atlasKey{}] = &atlasClient{c}
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"atlas-action": atlasAction,
			"mock-atlas":   mockAtlasOutput,
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
