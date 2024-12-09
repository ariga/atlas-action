// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/fatih/color"
)

type bbPipe struct {
	*coloredLogger
	getenv func(string) string
}

// New returns a new Action for GitHub Actions.
func NewBitBucketPipe(getenv func(string) string, w io.Writer) Action {
	color.NoColor = false // Enable color for Bitbucket.
	return &bbPipe{getenv: getenv, coloredLogger: &coloredLogger{w: w}}
}

// GetType implements Action.
func (a *bbPipe) GetType() atlasexec.TriggerType {
	// TODO: Add the new trigger type to the SDK.
	return atlasexec.TriggerType("BITBUCKET")
}

// GetTriggerContext implements Action.
func (a *bbPipe) GetTriggerContext() (*TriggerContext, error) {
	tc := &TriggerContext{
		Branch:  a.getenv("BITBUCKET_BRANCH"),
		Commit:  a.getenv("BITBUCKET_COMMIT"),
		Repo:    a.getenv("BITBUCKET_REPO_FULL_NAME"),
		RepoURL: a.getenv("BITBUCKET_GIT_HTTP_ORIGIN"),
		SCM: SCM{
			// TODO: Add the new SCM type to the SDK.
			Type:   atlasexec.SCMType("BITBUCKET"),
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
	return strings.TrimSpace(a.getenv(toEnvVar(name)))
}

// SetOutput implements Action.
func (a *bbPipe) SetOutput(name, value string) {
	dir := a.getenv("BITBUCKET_PIPE_STORAGE_DIR")
	if dir == "" {
		return
	}
	// Because Bitbucket Pipes do not support output variables,
	// we write the output to a file.
	// So the next step can read the outputs using the source command.
	// e.g:
	// ```shell
	// source $BITBUCKET_PIPE_SHARED_STORAGE_DIR/arigaio/atlas-action-<action>/outputs.sh
	// ```
	// https://support.atlassian.com/bitbucket-cloud/docs/advanced-techniques-for-writing-pipes/#Sharing-information-between-pipes
	if err := writeBashEnv(filepath.Join(dir, "outputs.sh"), name, value); err != nil {
		a.Errorf("failed to write output to file %s: %v", dir, err)
	}
	// TODO: Add JSON output support.
}

func (a *bbPipe) AddStepSummary(string) {}

// SCM implements Action.
func (a *bbPipe) SCM() (SCMClient, error) {
	return nil, errors.New("not implemented")
}

var _ Action = (*bbPipe)(nil)
