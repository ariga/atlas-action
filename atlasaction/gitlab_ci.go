// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ariga.io/atlas-action/internal/gitlab"
	"ariga.io/atlas-go-sdk/atlasexec"
)

// gitlabCI is an implementation of the Action interface for Gitlab CI.
type gitlabCI struct {
	*coloredLogger
	getenv func(string) string
}

// NewGitlabCI returns a new Action for Gitlab CI.
func NewGitlabCI(getenv func(string) string, w io.Writer) *gitlabCI {
	return &gitlabCI{getenv: getenv, coloredLogger: &coloredLogger{w}}
}

// GetType implements the Action interface.
func (*gitlabCI) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGitlab
}

// Getenv implements Action.
func (a *gitlabCI) Getenv(key string) string {
	return a.getenv(key)
}

// GetInput implements the Action interface.
func (a *gitlabCI) GetInput(name string) string {
	return strings.TrimSpace(a.getenv(toInputVarName(name)))
}

// SetOutput implements the Action interface.
func (a *gitlabCI) SetOutput(name, value string) {
	dotEnv := ".env"
	if dir := a.getenv("CI_PROJECT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			a.Errorf("failed to create output directory %s: %v", dir, err)
			return
		}
		dotEnv = filepath.Join(dir, dotEnv)
	}
	err := fprintln(dotEnv, toOutputVar(a.getenv("ATLAS_ACTION_COMMAND"), name, value))
	if err != nil {
		a.Errorf("failed to write output to file .env: %v", err)
	}
}

// GetTriggerContext implements the Action interface.
func (a *gitlabCI) GetTriggerContext(context.Context) (*TriggerContext, error) {
	ctx := &TriggerContext{
		Act:     a,
		SCM:     SCM{Type: atlasexec.SCMTypeGitlab, APIURL: a.getenv("CI_API_V4_URL")},
		Repo:    a.getenv("CI_PROJECT_NAME"),
		RepoURL: a.getenv("CI_PROJECT_URL"),
		Branch:  a.getenv("CI_COMMIT_REF_NAME"),
		Commit:  a.getenv("CI_COMMIT_SHA"),
		Actor:   &Actor{Name: a.getenv("GITLAB_USER_NAME"), ID: a.getenv("GITLAB_USER_ID")},
	}
	if mr := a.getenv("CI_MERGE_REQUEST_IID"); mr != "" {
		num, err := strconv.Atoi(mr)
		if err != nil {
			return nil, err
		}
		ctx.PullRequest = &PullRequest{
			Commit: a.getenv("CI_COMMIT_SHA"),
			Number: num,
			URL:    a.getenv("CI_MERGE_REQUEST_REF_PATH"),
			Body:   a.getenv("CI_MERGE_REQUEST_DESCRIPTION"),
		}
	}
	return ctx, nil
}

type glClient struct {
	*gitlab.Client
}

func GitLabClient(project, baseURL, token string) (*glClient, error) {
	c, err := gitlab.NewClient(project,
		gitlab.WithBaseURL(baseURL),
		gitlab.WithToken(token),
	)
	if err != nil {
		return nil, err
	}
	return &glClient{Client: c}, nil
}

// CommentLint implements SCMClient.
func (c *glClient) CommentLint(ctx context.Context, tc *TriggerContext, r *atlasexec.SummaryReport) error {
	comment, err := RenderTemplate("migrate-lint.tmpl", r)
	if err != nil {
		return err
	}
	return c.upsertComment(ctx, tc.PullRequest, tc.Act.GetInput("dir-name"), comment)
}

// CommentPlan implements SCMClient.
func (c *glClient) CommentPlan(ctx context.Context, tc *TriggerContext, p *atlasexec.SchemaPlan) error {
	// Report the schema plan to the user and add a comment to the PR.
	comment, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan": p,
	})
	if err != nil {
		return fmt.Errorf("failed to generate schema plan comment: %w", err)
	}
	return c.upsertComment(ctx, tc.PullRequest, p.File.Name, comment)
}

func (c *glClient) upsertComment(ctx context.Context, pr *PullRequest, id, comment string) error {
	comments, err := c.PullRequestNotes(ctx, pr.Number)
	if err != nil {
		return err
	}
	marker := commentMarker(id)
	comment += "\n" + marker
	if found := slices.IndexFunc(comments, func(c gitlab.Note) bool {
		return !c.System && strings.Contains(c.Body, marker)
	}); found != -1 {
		return c.UpdateNote(ctx, pr.Number, comments[found].ID, comment)
	}
	return c.CreateNote(ctx, pr.Number, comment)
}

func (c *glClient) IsCoAuthored(context.Context, *TriggerContext) (bool, error) {
	// Not implemented.
	return false, nil
}

var _ Action = (*gitlabCI)(nil)
var _ SCMClient = (*glClient)(nil)
