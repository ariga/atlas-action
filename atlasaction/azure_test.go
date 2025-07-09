// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/stretchr/testify/require"
)

func TestAzureDevOpsTask(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "azure"),
		Setup: func(e *testscript.Env) (err error) {
			e.Setenv("MOCK_ATLAS", filepath.Join(wd, "mock-atlas.sh"))
			e.Setenv("TF_BUILD", "True")
			e.Setenv("BUILD_REPOSITORY_PROVIDER", "GitHub")
			e.Setenv("BUILD_SOURCESDIRECTORY", e.WorkDir)
			return nil
		},
	})
}

func TestAuthEndpoint(t *testing.T) {
	m := map[string]string{
		"ENDPOINT_AUTH_oauth": `{"scheme":"OAuth","parameters":{"AccessToken":"oauth-token"}}`,
		"ENDPOINT_AUTH_pat":   `{"scheme":"PersonalAccessToken","parameters":{"accessToken":"my-token"}}`,
		"ENDPOINT_AUTH_token": `{"scheme":"Token","parameters":{"AccessToken":"token"}}`,

		"ENDPOINT_AUTH_invalid-oauth": `{"scheme":"OAuth","parameters":{"foo":"bar"}}`,
		"ENDPOINT_AUTH_invalid-pat":   `{"scheme":"PersonalAccessToken","parameters":{"foo":"bar"}}`,
		"ENDPOINT_AUTH_invalid-token": `{"scheme":"Token","parameters":{"foo":"bar"}}`,
	}
	a := NewAzure(func(s string) string { return m[s] }, io.Discard)

	tok, err := a.getGHToken("oauth")
	require.NoError(t, err)
	require.Equal(t, "oauth-token", tok)

	tok, err = a.getGHToken("pat")
	require.NoError(t, err)
	require.Equal(t, "my-token", tok)

	tok, err = a.getGHToken("token")
	require.NoError(t, err)
	require.Equal(t, "token", tok)

	_, err = a.getGHToken("unknown")
	require.ErrorContains(t, err, "ENDPOINT_AUTH_unknown is not set")

	_, err = a.getGHToken("invalid-oauth")
	require.ErrorContains(t, err, "missing AccessToken in ENDPOINT_AUTH_invalid-oauth")
	_, err = a.getGHToken("invalid-pat")
	require.ErrorContains(t, err, "missing accessToken in ENDPOINT_AUTH_invalid-pat")
	_, err = a.getGHToken("invalid-token")
	require.ErrorContains(t, err, "missing AccessToken in ENDPOINT_AUTH_invalid-token")
}
