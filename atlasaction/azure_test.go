// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction_test

import (
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
