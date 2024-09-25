// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"ariga.io/atlas-go-sdk/atlasexec"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// gitlabCI is an implementation of the Action interface for GitHub Actions.
type gitlabCI struct {
	w      io.Writer
	getenv func(string) string
}

var _ Action = (*gitlabCI)(nil)

// NewGitlabCI returns a new Action for Gitlab CI.
func NewGitlabCI(getenv func(string) string, w io.Writer) Action {
	return &gitlabCI{getenv: getenv, w: w}
}

// GetType implements the Action interface.
func (g *gitlabCI) GetType() atlasexec.TriggerType {
	return "GITLAB_CI"
}

// GetInput implements the Action interface.
func (g *gitlabCI) GetInput(name string) string {
	e := strings.ReplaceAll(name, " ", "_")
	e = strings.ReplaceAll(e, "-", "_")
	e = strings.ToUpper(e)
	return strings.TrimSpace(g.getenv(e))
}

// SetOutput implements the Action interface.
func (g *gitlabCI) SetOutput(name, value string) {
	f, err := os.OpenFile(".env", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(fmt.Sprintf("%s=%s\n", name, value))
}

// GetTriggerContext implements the Action interface.
func (g *gitlabCI) GetTriggerContext() (*TriggerContext, error) {
	ctx := &TriggerContext{
		SCM: SCM{
			Type:   atlasexec.SCMTypeGithub, // TODO: Change to Gitlab.
			APIURL: g.getenv("CI_API_V4_URL"),
		},
		Repo:    g.getenv("CI_PROJECT_NAME"),
		RepoURL: g.getenv("CI_PROJECT_URL"),
		Branch:  g.getenv("CI_COMMIT_REF_NAME"),
		Commit:  g.getenv("CI_COMMIT_SHA"),
		Actor:   &Actor{Name: g.getenv("GITLAB_USER_NAME"), ID: g.getenv("GITLAB_USER_ID")},
	}
	if mr := g.getenv("CI_MERGE_REQUEST_IID"); mr != "" {
		num, err := strconv.Atoi(mr)
		if err != nil {
			return nil, err
		}
		ctx.PullRequest = &PullRequest{
			Commit: g.getenv("CI_COMMIT_SHA"),
			Number: num,
			URL:    g.getenv("CI_MERGE_REQUEST_REF_PATH"),
		}
	}
	return ctx, nil
}

// Infof implements the Logger interface.
func (g *gitlabCI) Infof(msg string, args ...any) {
	fmt.Fprintf(g.w, msg+EOF, args...)
}

// Warningf implements the Logger interface.
func (g *gitlabCI) Warningf(msg string, args ...any) {
	fmt.Fprintf(g.w, msg+EOF, args...)
}

// Errorf implements the Logger interface.
func (g *gitlabCI) Errorf(msg string, args ...any) {
	fmt.Fprintf(g.w, msg+EOF, args...)
}

// Fatalf implements the Logger interface.
func (g *gitlabCI) Fatalf(msg string, args ...any) {
	g.Errorf(msg, args...)
	os.Exit(1)
}

// WithFieldsMap implements the Logger interface.
func (g *gitlabCI) WithFieldsMap(map[string]string) Logger {
	// unsupported
	return g
}

// AddStepSummary implements the Action interface.
func (g *gitlabCI) AddStepSummary(summary string) {
	// unsupported
}
