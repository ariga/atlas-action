// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/alecthomas/kong"
)

const (
	CmdMigratePush  = "migrate/push"
	CmdMigrateLint  = "migrate/lint"
	CmdMigrateApply = "migrate/apply"
	CmdMigrateDown  = "migrate/down"
)

var cli RunAction

func main() {
	action, err := newAction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to run action in the current environment: %s\n", err)
		os.Exit(1)
	}
	c, err := atlasexec.NewClient("", "atlas")
	if err != nil {
		action.Fatalf("Failed to create client: %s", err)
	}
	ctx := context.Background()
	cli := kong.Parse(
		&cli,
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(c),
		kong.BindTo(action, (*atlasaction.Action)(nil)),
	)
	if err := cli.Run(); err != nil {
		if uerr := errors.Unwrap(err); uerr != nil {
			err = uerr
		}
		action.Fatalf(err.Error())
	}
}

// VersionFlag is a flag type that can be used to display a version number, stored in the "version" variable.
type VersionFlag bool

// BeforeReset writes the version variable and terminates with a 0 exit status.
func (v VersionFlag) BeforeReset(app *kong.Kong) error {
	_, err := fmt.Fprintln(app.Stdout, atlasaction.Version)
	app.Exit(0)
	return err
}

// RunAction is a command to run one of the Atlas GitHub Actions.
type RunAction struct {
	Action  string      `help:"Command to run" required:""`
	Version VersionFlag `help:"Prints the version and exits"`
}

func (r *RunAction) Run(ctx context.Context, client *atlasexec.Client, action atlasaction.Action) error {
	_ = os.Setenv("ATLAS_ACTION_COMMAND", r.Action)
	defer func() {
		_ = os.Unsetenv("ATLAS_ACTION_COMMAND")
	}()
	if action.GetInput("working-directory") != "" {
		if err := os.Chdir(action.GetInput("working-directory")); err != nil {
			return fmt.Errorf("failed to change working directory: %w", err)
		}
	}
	switch r.Action {
	case CmdMigrateApply:
		return atlasaction.MigrateApply(ctx, client, action)
	case CmdMigrateDown:
		return atlasaction.MigrateDown(ctx, client, action)
	case CmdMigratePush:
		return atlasaction.MigratePush(ctx, client, action)
	case CmdMigrateLint:
		return atlasaction.MigrateLint(ctx, client, action)
	}
	return fmt.Errorf("unknown action: %s", r.Action)
}

// newAction creates a new atlasaction.Action based on the environment.
func newAction() (atlasaction.Action, error) {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return atlasaction.NewGHAction(), nil
	}
	if os.Getenv("CIRCLECI") == "true" {
		return atlasaction.NewOrb(), nil
	}
	return nil, errors.New("unsupported environment")
}
