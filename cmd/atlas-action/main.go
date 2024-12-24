// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package main

import (
	"context"
	"os"

	"ariga.io/atlas-action/internal/cmdapi"
)

var (
	// version holds atlas-action version. When built with cloud packages should be set by build flag, e.g.
	// "-X 'main.version=v0.1.2'"
	version string = "v0.0.0"
	// commit holds the git commit hash. When built with cloud packages should be set by build flag, e.g.
	// "-X 'main.commit=abcdef1234'"
	commit string = "dev"
)

func main() {
	os.Exit(cmdapi.Main(context.Background(), version, commit))
}
