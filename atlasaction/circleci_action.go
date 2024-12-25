// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"fmt"
	"io"
	"strings"

	"ariga.io/atlas-go-sdk/atlasexec"
)

// circleciOrb is an implementation of the Action interface for GitHub Actions.
type circleCIOrb struct {
	*coloredLogger
	getenv func(string) string
}

var _ Action = (*circleCIOrb)(nil)

// New returns a new Action for GitHub Actions.
func NewCircleCIOrb(getenv func(string) string, w io.Writer) Action {
	return &circleCIOrb{getenv: getenv, coloredLogger: &coloredLogger{w}}
}

// GetType implements the Action interface.
func (a *circleCIOrb) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeCircleCIOrb
}

// Getenv implements Action.
func (a *circleCIOrb) Getenv(key string) string {
	return a.getenv(key)
}

// GetInput implements the Action interface.
func (a *circleCIOrb) GetInput(name string) string {
	return strings.TrimSpace(a.getenv(toEnvVar("INPUT_" + name)))
}

// SetOutput implements the Action interface.
func (a *circleCIOrb) SetOutput(name, value string) {
	if bashEnv := a.getenv("BASH_ENV"); bashEnv != "" {
		err := fprintln(bashEnv,
			"export", toOutputVar(a.getenv("ATLAS_ACTION_COMMAND"), name, value))
		if err != nil {
			a.Fatalf("failed to write env to file %s: %v", bashEnv, err)
		}
		return
	}
}

// GetTriggerContext implements the Action interface.
// https://circleci.com/docs/variables/#built-in-environment-variables
func (a *circleCIOrb) GetTriggerContext(ctx context.Context) (*TriggerContext, error) {
	tc := &TriggerContext{
		RepoURL: a.getenv("CIRCLE_REPOSITORY_URL"),
		Repo:    a.getenv("CIRCLE_PROJECT_REPONAME"),
		Branch:  a.getenv("CIRCLE_BRANCH"),
		Commit:  a.getenv("CIRCLE_SHA1"),
	}
	if tc.Repo == "" {
		return nil, fmt.Errorf("missing CIRCLE_PROJECT_REPONAME environment variable")
	}
	if tc.Commit == "" {
		return nil, fmt.Errorf("missing CIRCLE_SHA1 environment variable")
	}
	// Detect SCM provider based on Token.
	switch ghToken := a.getenv("GITHUB_TOKEN"); {
	case ghToken != "":
		tc.SCM = SCM{
			Type:   atlasexec.SCMTypeGithub,
			APIURL: a.getenv("GITHUB_API_URL"),
		}
		// Used to change the location that the linting results are posted to.
		// If GITHUB_REPOSITORY is not set, we default to the CIRCLE_PROJECT_REPONAME repo.
		if v := a.getenv("GITHUB_REPOSITORY"); v != "" {
			tc.Repo = v
		}
		// CIRCLE_REPOSITORY_URL will be empty for some reason, causing ctx.RepoURL to be empty.
		// In this case, we default to the GitHub Cloud URL.
		if tc.RepoURL == "" {
			tc.RepoURL = fmt.Sprintf("https://github.com/%s", tc.Repo)
		}
		// CIRCLE_BRANCH will be empty when the event is triggered by a tag.
		// In this case, we can use CIRCLE_TAG as the branch.
		if tc.Branch == "" {
			tag := a.getenv("CIRCLE_TAG")
			if tag == "" {
				return nil, fmt.Errorf("cannot determine branch due to missing CIRCLE_BRANCH and CIRCLE_TAG environment variables")
			}
			tc.Branch = tag
			return tc, nil
		}
		c, err := GitHubClient(tc.Repo, tc.SCM.APIURL, ghToken)
		if err != nil {
			return nil, fmt.Errorf("failed to create GitHub client: %w", err)
		}
		pr, err := c.OpeningPullRequest(ctx, tc.Branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get open pull requests: %w", err)
		}
		tc.PullRequest = &PullRequest{
			Number: pr.Number,
			URL:    pr.URL,
			Commit: pr.Commit,
		}
	}
	return tc, nil
}
