// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package cmdapi_test

import (
	"context"
	"os"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-action/internal/cmdapi"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/stretchr/testify/require"
)

func TestRunAction_Run(t *testing.T) {
	client, err := atlasexec.NewClient("", "atlas")
	require.NoError(t, err)
	act := atlasaction.NewGitHub(os.Getenv, os.Stdout)
	t.Run("fake", func(t *testing.T) {
		r := &cmdapi.RunActionCmd{Action: "fake"}
		c, err := atlasaction.New(atlasaction.WithAction(act), atlasaction.WithAtlas(client))
		require.NoError(t, err)
		err = r.Run(context.Background(), c)
		require.EqualError(t, err, "unknown action: fake")
	})
}
