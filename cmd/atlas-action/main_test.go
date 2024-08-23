// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"testing"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/stretchr/testify/require"
)

func TestRunAction_Run(t *testing.T) {
	client, err := atlasexec.NewClient("", "atlas")
	require.NoError(t, err)
	act := atlasaction.NewGHAction()

	t.Run("fake", func(t *testing.T) {
		r := &RunActionCmd{
			Action: "fake",
		}
		err := r.Run(context.Background(), &atlasaction.Actions{Action: act, Atlas: client})
		require.EqualError(t, err, "unknown action: fake")
	})
}
