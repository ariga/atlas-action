// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"fmt"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/alecthomas/kong"
	"github.com/sethvargo/go-githubactions"
)

const (
	CmdMigratePush  = "migrate/push"
	CmdMigrateLint  = "migrate/lint"
	CmdMigrateApply = "migrate/apply"
)

var cli RunAction

func main() {
	action := githubactions.New()
	c, err := atlasexec.NewClient("", "atlas")
	if err != nil {
		action.Fatalf("Failed to create client: %s", err)
	}
	cli := kong.Parse(&cli, kong.Bind(context.Background(), c, action))
	if err := cli.Run(); err != nil {
		action.Fatalf("Failed to run command: %s", err)
	}
}

// RunAction is a command to run one of the Atlas GitHub Actions.
type RunAction struct {
	Action string `help:"Command to run" required:""`
}

func (r *RunAction) Run(ctx context.Context, client *atlasexec.Client, action *githubactions.Action) error {
	switch r.Action {
	case CmdMigrateApply:
		return atlasaction.MigrateApply(ctx, client, action)
	case CmdMigrateLint, CmdMigratePush:
		return fmt.Errorf("not implemented: %s", r.Action)
	}
	return fmt.Errorf("unknown action: %s", r.Action)
}
