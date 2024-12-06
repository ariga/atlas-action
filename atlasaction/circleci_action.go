// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
	"golang.org/x/oauth2"
)

// circleciOrb is an implementation of the Action interface for GitHub Actions.
type circleCIOrb struct {
	*colorsLogger
	getenv func(string) string
}

var _ Action = (*circleCIOrb)(nil)

// New returns a new Action for GitHub Actions.
func NewCircleCIOrb(getenv func(string) string, w io.Writer) Action {
	return &circleCIOrb{getenv: getenv, colorsLogger: &colorsLogger{w}}
}

// GetType implements the Action interface.
func (a *circleCIOrb) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeCircleCIOrb
}

// GetInput implements the Action interface.
func (a *circleCIOrb) GetInput(name string) string {
	return strings.TrimSpace(a.getenv(toEnvVar("INPUT_" + name)))
}

// SetOutput implements the Action interface.
func (a *circleCIOrb) SetOutput(name, value string) {
	if bashEnv := a.getenv("BASH_ENV"); bashEnv != "" {
		cmd := a.getenv("ATLAS_ACTION_COMMAND")
		err := writeBashEnv(bashEnv, toEnvVar(
			fmt.Sprintf("ATLAS_OUTPUT_%s_%s", cmd, name)), value)
		if err != nil {
			a.Fatalf("failed to write env to file %s: %v", bashEnv, err)
		}
		return
	}
}

// GetTriggerContext implements the Action interface.
// https://circleci.com/docs/variables/#built-in-environment-variables
func (a *circleCIOrb) GetTriggerContext() (*TriggerContext, error) {
	ctx := &TriggerContext{}
	if ctx.Repo = a.getenv("CIRCLE_PROJECT_REPONAME"); ctx.Repo == "" {
		return nil, fmt.Errorf("missing CIRCLE_PROJECT_REPONAME environment variable")
	}
	ctx.RepoURL = a.getenv("CIRCLE_REPOSITORY_URL")
	ctx.Branch = a.getenv("CIRCLE_BRANCH")
	if ctx.Commit = a.getenv("CIRCLE_SHA1"); ctx.Commit == "" {
		return nil, fmt.Errorf("missing CIRCLE_SHA1 environment variable")
	}
	// Detect SCM provider based on Token.
	switch ghToken := a.getenv("GITHUB_TOKEN"); {
	case ghToken != "":
		ctx.SCM = SCM{
			Type:   atlasexec.SCMTypeGithub,
			APIURL: defaultGHApiUrl,
		}
		if v := a.getenv("GITHUB_API_URL"); v != "" {
			ctx.SCM.APIURL = v
		}
		// Used to change the location that the linting results are posted to.
		// If GITHUB_REPOSITORY is not set, we default to the CIRCLE_PROJECT_REPONAME repo.
		if v := a.getenv("GITHUB_REPOSITORY"); v != "" {
			ctx.Repo = v
		}
		// CIRCLE_REPOSITORY_URL will be empty for some reason, causing ctx.RepoURL to be empty.
		// In this case, we default to the GitHub Cloud URL.
		if ctx.RepoURL == "" {
			ctx.RepoURL = fmt.Sprintf("https://github.com/%s", ctx.Repo)
		}
		// CIRCLE_BRANCH will be empty when the event is triggered by a tag.
		// In this case, we can use CIRCLE_TAG as the branch.
		if ctx.Branch == "" {
			tag := a.getenv("CIRCLE_TAG")
			if tag == "" {
				return nil, fmt.Errorf("cannot determine branch due to missing CIRCLE_BRANCH and CIRCLE_TAG environment variables")
			}
			ctx.Branch = tag
			return ctx, nil
		}
		// get open pull requests for the branch.
		c := &githubAPI{
			client: &http.Client{
				Timeout: time.Second * 30,
				Transport: &oauth2.Transport{
					Source: oauth2.StaticTokenSource(&oauth2.Token{
						AccessToken: ghToken,
					}),
				},
			},
			baseURL: ctx.SCM.APIURL,
			repo:    ctx.Repo,
		}
		var err error
		ctx.PullRequest, err = c.OpeningPullRequest(context.Background(), ctx.Branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get open pull requests: %w", err)
		}
	}
	return ctx, nil
}

// AddStepSummary implements the Action interface.
func (a *circleCIOrb) AddStepSummary(summary string) {
	// unsupported
}

func (a *circleCIOrb) SCM() (SCMClient, error) {
	tc, err := a.GetTriggerContext()
	if err != nil {
		return nil, err
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		a.Warningf("GITHUB_TOKEN is not set, the action may not have all the permissions")
	}
	return githubClient(tc.Repo, tc.SCM.APIURL, token), nil
}
