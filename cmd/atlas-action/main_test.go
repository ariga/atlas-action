package main

import (
	"context"
	"testing"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/sethvargo/go-githubactions"
	"github.com/stretchr/testify/require"
)

func TestRunAction_Run(t *testing.T) {
	client, err := atlasexec.NewClient("", "atlas")
	require.NoError(t, err)
	act := githubactions.New()

	t.Run("fake", func(t *testing.T) {
		r := &RunAction{
			Action: "fake",
		}
		err := r.Run(context.Background(), client, act)
		require.EqualError(t, err, "unknown action: fake")
	})
	t.Run(CmdMigrateApply, func(t *testing.T) {
		r := &RunAction{
			Action: CmdMigrateApply,
		}
		err := r.Run(context.Background(), client, act)
		require.EqualError(t, err, "not implemented: migrate/apply")
	})
}
