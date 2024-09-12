// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"github.com/stretchr/testify/require"
)

func Test_circleCIOrb_GetTriggerContext(t *testing.T) {
	orb := atlasaction.NewCircleCIOrb(os.Getenv, os.Stdout)
	_, err := orb.GetTriggerContext()
	require.EqualError(t, err, "missing CIRCLE_PROJECT_REPONAME environment variable")
	t.Setenv("CIRCLE_PROJECT_REPONAME", "atlas-orb")
	_, err = orb.GetTriggerContext()
	require.EqualError(t, err, "missing CIRCLE_SHA1 environment variable")
	t.Setenv("CIRCLE_SHA1", "1234567890")
	ctx, err := orb.GetTriggerContext()
	require.NoError(t, err)
	require.Equal(t, &atlasaction.TriggerContext{
		Repo:   "atlas-orb",
		Commit: "1234567890",
	}, ctx)
	t.Setenv("GITHUB_TOKEN", "1234567890")
	t.Setenv("GITHUB_REPOSITORY", "ariga/atlas-orb")
	_, err = orb.GetTriggerContext()
	require.EqualError(t, err, "cannot determine branch due to missing CIRCLE_BRANCH and CIRCLE_TAG environment variables")
	t.Setenv("CIRCLE_BRANCH", "main")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/ariga/atlas-orb/pulls", r.URL.Path)
		require.Equal(t, "GET", r.Method)
		require.Equal(t, "state=open&head=ariga:main&sort=created&direction=desc&per_page=1&page=1", r.URL.RawQuery)
		w.Write([]byte(`
		[
			{"number":1,"url":"https://api.github.com/repos/ariga/atlas-orb/pulls/9","head":{"sha":"1234567890"}},
			{"number":2,"url":"https://api.github.com/repos/ariga/atlas-orb/pulls/8","head":{"sha":"1234567890"}}
		]`))
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)
	ctx, err = orb.GetTriggerContext()
	require.NoError(t, err)
	require.Equal(t, &atlasaction.PullRequest{
		Number: 1,
		URL:    "https://api.github.com/repos/ariga/atlas-orb/pulls/9",
		Commit: "1234567890",
	}, ctx.PullRequest)
}
