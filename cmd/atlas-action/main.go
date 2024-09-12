// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/alecthomas/kong"
)

const (
	// Versioned workflow Commands
	CmdMigratePush  = "migrate/push"
	CmdMigrateLint  = "migrate/lint"
	CmdMigrateApply = "migrate/apply"
	CmdMigrateDown  = "migrate/down"
	CmdMigrateTest  = "migrate/test"
	// Declarative workflow Commands
	CmdSchemaPush = "schema/push"
	CmdSchemaTest = "schema/test"
	CmdSchemaPlan = "schema/plan"
)

var (
	// version holds atlas-action version. When built with cloud packages should be set by build flag, e.g.
	// "-X 'main.version=v0.1.2'"
	version string
	// commit holds the git commit hash. When built with cloud packages should be set by build flag, e.g.
	// "-X 'main.commit=abcdef1234'"
	commit string = "dev"
)

func main() {
	action, err := newAction(os.Getenv, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to run action in the current environment: %s\n", err)
		os.Exit(1)
	}
	atlas, err := atlasexec.NewClient("", "atlas")
	if err != nil {
		action.Fatalf("Failed to create client: %s", err)
	}
	cli := kong.Parse(
		&RunActionCmd{},
		kong.BindTo(context.Background(), (*context.Context)(nil)),
	)
	if err := cli.Run(&atlasaction.Actions{
		Action:  action,
		Version: version,
		Atlas:   atlas,
	}); err != nil {
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
	_, err := fmt.Fprintf(app.Stdout, "%s-%s\n", version, commit)
	app.Exit(0)
	return err
}

// RunActionCmd is a command to run one of the Atlas GitHub Actions.
type RunActionCmd struct {
	Action  string      `help:"Command to run" required:""`
	Version VersionFlag `help:"Prints the version and exits"`
}

func (r *RunActionCmd) Run(ctx context.Context, a *atlasaction.Actions) error {
	_ = os.Setenv("ATLAS_ACTION_COMMAND", r.Action)
	defer func() {
		_ = os.Unsetenv("ATLAS_ACTION_COMMAND")
	}()
	// Set the working directory if provided.
	if dir := a.WorkingDir(); dir != "" {
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("failed to change working directory: %w", err)
		}
	}
	switch r.Action {
	case CmdMigrateApply:
		return a.MigrateApply(ctx)
	case CmdMigrateDown:
		return a.MigrateDown(ctx)
	case CmdMigratePush:
		return a.MigratePush(ctx)
	case CmdMigrateLint:
		return a.MigrateLint(ctx)
	case CmdMigrateTest:
		return a.MigrateTest(ctx)
	case CmdSchemaPush:
		return a.SchemaPush(ctx)
	case CmdSchemaTest:
		return a.SchemaTest(ctx)
	case CmdSchemaPlan:
		return a.SchemaPlan(ctx)
	default:
		return fmt.Errorf("unknown action: %s", r.Action)
	}
}

// newAction creates a new atlasaction.Action based on the environment.
func newAction(getenv func(string) string, w io.Writer) (atlasaction.Action, error) {
	if getenv("GITHUB_ACTIONS") == "true" {
		return atlasaction.NewGHAction(getenv, w), nil
	}
	if getenv("CIRCLECI") == "true" {
		return atlasaction.NewCircleCIOrb(getenv, w), nil
	}
	return nil, errors.New("unsupported environment")
}
