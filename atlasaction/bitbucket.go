// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/fatih/color"
)

type bbPipe struct {
	*coloredLogger
	getenv func(string) string
}

// NewBitBucketPipe returns a new Action for BitBucket.
func NewBitBucketPipe(getenv func(string) string, w io.Writer) Action {
	// Disable color output for testing,
	// but enable it for non-testing environments.
	color.NoColor = testing.Testing()
	return &bbPipe{getenv: getenv, coloredLogger: &coloredLogger{w: w}}
}

// GetType implements Action.
func (a *bbPipe) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeBitbucket
}

// GetTriggerContext implements Action.
func (a *bbPipe) GetTriggerContext() (*TriggerContext, error) {
	tc := &TriggerContext{
		Branch:  a.getenv("BITBUCKET_BRANCH"),
		Commit:  a.getenv("BITBUCKET_COMMIT"),
		Repo:    a.getenv("BITBUCKET_REPO_FULL_NAME"),
		RepoURL: a.getenv("BITBUCKET_GIT_HTTP_ORIGIN"),
		SCM: SCM{
			Type:   atlasexec.SCMTypeBitbucket,
			APIURL: "https://api.bitbucket.org/2.0",
		},
	}
	if pr := a.getenv("BITBUCKET_PR_ID"); pr != "" {
		var err error
		tc.PullRequest = &PullRequest{
			Commit: a.getenv("BITBUCKET_COMMIT"),
		}
		tc.PullRequest.Number, err = strconv.Atoi(pr)
		if err != nil {
			return nil, err
		}
		// <repo-url>/pull-requests/<pr-id>
		tc.PullRequest.URL, err = url.JoinPath(tc.RepoURL, "pull-requests", pr)
		if err != nil {
			return nil, err
		}
	}
	return tc, nil
}

// GetInput implements the Action interface.
func (a *bbPipe) GetInput(name string) string {
	return strings.TrimSpace(a.getenv("ATLAS_INPUT_" + toEnvVar(name)))
}

// SetOutput implements Action.
func (a *bbPipe) SetOutput(name, value string) {
	// Because Bitbucket Pipes does not support output variables,
	// we write the output to a file.
	// So the next step can read the outputs using the source command.
	// e.g:
	// ```shell
	// source $BITBUCKET_PIPE_STORAGE_DIR/outputs.sh
	// ```
	// https://support.atlassian.com/bitbucket-cloud/docs/advanced-techniques-for-writing-pipes/#Sharing-information-between-pipes
	dir := a.getenv("BITBUCKET_PIPE_STORAGE_DIR")
	if out := a.getenv("OUTPUT_DIR"); out != "" {
		// The user can set the output directory using
		// the OUTPUT_DIR environment variable.
		// This is useful when the user wants to share the output
		// with steps run outside the pipe.
		dir = out
	}
	if dir == "" {
		return
	}
	cmd := a.getenv("ATLAS_ACTION_COMMAND")
	err := writeBashEnv(filepath.Join(dir, "outputs.sh"), toEnvVar(
		fmt.Sprintf("ATLAS_OUTPUT_%s_%s", cmd, name)), value)
	if err != nil {
		a.Errorf("failed to write output to file %s: %v", dir, err)
	}
}

func (a *bbPipe) AddStepSummary(string) {}

var _ Action = (*bbPipe)(nil)
