// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package cmdapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"ariga.io/atlas-action/atlasaction"
	"ariga.io/atlas-action/atlasaction/cloud"
	"github.com/alecthomas/kong"
)

func Main(ctx context.Context, version, commit string) int {
	atlasPath := "atlas"
	if p := os.Getenv("ATLAS_PATH"); p != "" {
		// The environment can provide the path to the atlas-cli binary.
		//
		// This is useful when running the action in a Docker container,
		// and the user can mount the atlas-cli binary in the container and set the
		// ATLAS_PATH environment variable to the path of the binary.
		atlasPath = p
	}
	act, err := atlasaction.New(
		atlasaction.WithAtlasPath(atlasPath),
		atlasaction.WithCloudClient(cloud.New),
		atlasaction.WithCmdExecutor(exec.CommandContext),
		atlasaction.WithVersion(version),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to run action in the current environment: %s\n", err)
		os.Exit(1)
	}
	cli := kong.Parse(
		&RunActionCmd{},
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Vars{
			"version": fmt.Sprintf("atlas-action version %s-%s\n", version, commit),
		},
	)
	if err := cli.Run(act); err != nil {
		if uerr := errors.Unwrap(err); uerr != nil {
			err = uerr
		}
		act.Fatalf(err.Error())
	}
	return 0
}

// RunActionCmd is a command to run one of the Atlas GitHub Actions.
type RunActionCmd struct {
	Action  string           `help:"Command to run" required:"" env:"ATLAS_ACTION"`
	Version kong.VersionFlag `name:"version" help:"Print version information and quit"`
}

func (r *RunActionCmd) Run(ctx context.Context, a *atlasaction.Actions) error {
	_ = os.Setenv("ATLAS_ACTION_COMMAND", r.Action)
	defer func() {
		_ = os.Unsetenv("ATLAS_ACTION_COMMAND")
	}()
	return a.Run(ctx, r.Action)
}
