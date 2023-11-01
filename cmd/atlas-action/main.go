// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"errors"
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

var Version string

func main() {
	action := githubactions.New()
	c, err := atlasexec.NewClient("", "atlas")
	if err != nil {
		action.Fatalf("Failed to create client: %s", err)
	}
	ctx := context.WithValue(context.Background(), atlasaction.VersionContextKey{}, Version)
	cli := kong.Parse(
		&cli,
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(c),
		kong.Bind(action),
	)
	if err := cli.Run(); err != nil {
		if uerr := errors.Unwrap(err); uerr != nil {
			err = uerr
		}
		action.Fatalf(err.Error())
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
	case CmdMigratePush:
		return atlasaction.MigratePush(ctx, client, action)
	case CmdMigrateLint:
		return atlasaction.MigrateLint(ctx, client, action)
	}
	return fmt.Errorf("unknown action: %s", r.Action)
}
