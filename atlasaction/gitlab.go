// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ariga.io/atlas-action/internal/gitlab"
	"ariga.io/atlas/atlasexec"
)

// GitLab is an implementation of the Action interface for Gitlab CI.
type GitLab struct {
	*coloredLogger
	getenv func(string) string
}

// NewGitlab returns a new Action for Gitlab CI.
func NewGitlab(getenv func(string) string, w io.Writer) *GitLab {
	// GitLab shows the colors of a job log. NO_COLOR turns them off, as
	// https://no-color.org asks.
	return &GitLab{getenv: getenv, coloredLogger: &coloredLogger{w: w, colors: getenv("NO_COLOR") == ""}}
}

// GetType implements the Action interface.
func (*GitLab) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGitlab
}

// Getenv implements Action.
func (a *GitLab) Getenv(key string) string {
	return a.getenv(key)
}

// GetInput implements the Action interface.
func (a *GitLab) GetInput(name string) string {
	v := strings.TrimSpace(a.getenv(toInputVarName(name)))
	if v != "" {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
	}
	return v
}

// SetOutput implements the Action interface.
func (a *GitLab) SetOutput(name, value string) {
	dotEnv := ".env"
	if dir := a.getenv("CI_PROJECT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			a.Errorf("failed to create output directory %s: %v", dir, err)
			return
		}
		dotEnv = filepath.Join(dir, dotEnv)
	}
	err := fprintln(dotEnv, toDotenvVar(a.getenv("ATLAS_ACTION_COMMAND"), name, value))
	if err != nil {
		a.Errorf("failed to write output to file .env: %v", err)
	}
}

// junitDriftReport is the JUnit report of migrate/drift, relative to the project
// directory. The template uploads it as artifacts:reports:junit.
const junitDriftReport = ".atlas-action/migrate-drift.junit.xml"

// MigrateDrift implements MigrateDriftReporter. It writes a JUnit report with a
// test case per checked database, shown in the Tests tab of the pipeline.
func (a *GitLab) MigrateDrift(_ context.Context, reports []*atlasexec.MigrateDrift) {
	name := filepath.Join(a.getenv("CI_PROJECT_DIR"), junitDriftReport)
	if err := writeDriftJUnit(name, reports); err != nil {
		a.Errorf("failed to write the JUnit report: %v", err)
	}
}

type (
	// junitSuite is a JUnit test suite, with the elements and attributes
	// GitLab reads. See https://docs.gitlab.com/ci/testing/unit_test_reports/.
	// Bitbucket Pipelines takes the counts from the tests, failures and errors
	// attributes, and shows no test of a suite without them.
	junitSuite struct {
		XMLName  xml.Name    `xml:"testsuite"`
		Name     string      `xml:"name,attr"`
		Tests    int         `xml:"tests,attr"`
		Failures int         `xml:"failures,attr"`
		Errors   int         `xml:"errors,attr"`
		Cases    []junitCase `xml:"testcase"`
	}
	// junitCase is a test case. encoding/xml escapes its text and replaces the
	// characters XML does not allow, such as the ANSI escape, with U+FFFD: one of
	// them would fail the parsing of the whole report.
	junitCase struct {
		Name      string `xml:"name,attr"`
		Classname string `xml:"classname,attr"`
		Failure   string `xml:"failure,omitempty"`
		Error     string `xml:"error,omitempty"`
	}
)

// writeDriftJUnit writes the drift reports to the named file as a JUnit test
// suite. A drifted database is a failure, and one that could not be checked is
// an error, which GitLab lists separately and keeps out of the failure history.
func writeDriftJUnit(name string, reports []*atlasexec.MigrateDrift) error {
	s := &junitSuite{Name: "atlas migrate drift", Tests: len(reports)}
	for _, r := range reports {
		c := junitCase{Name: r.URL, Classname: r.Dir}
		switch {
		case r.Error != "":
			s.Errors++
			c.Error = fmt.Sprintf("%s could not be checked: %s", r.URL, r.Error)
		case r.Drifted:
			s.Failures++
			// As the GitHub summary says, the statements describe the drift; they are not to be run.
			lines := []string{driftHeader(r), "", "The database diverged from the expected state as if the following were executed:"}
			c.Failure = strings.Join(append(lines, driftLines(r)...), "\n")
		}
		s.Cases = append(s.Cases, c)
	}
	b, err := xml.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	return os.WriteFile(name, append([]byte(xml.Header), append(b, '\n')...), 0644)
}

// GetTriggerContext implements the Action interface.
func (a *GitLab) GetTriggerContext(context.Context) (*TriggerContext, error) {
	ctx := &TriggerContext{
		Act:     a,
		SCMType: atlasexec.SCMTypeGitlab,
		SCMClient: func() (SCMClient, error) {
			envName := a.Getenv("ATLAS_GITLAB_TOKEN_ENV")
			if envName == "" {
				envName = "GITLAB_TOKEN"
			}
			token := a.Getenv(envName)
			if token == "" {
				a.Warningf("%s is not set, the action may not have all the permissions", envName)
			}
			return NewGitLabClient(
				a.Getenv("CI_PROJECT_ID"),
				a.getenv("CI_API_V4_URL"),
				token,
			)
		},
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

type GitLabClient struct {
	*gitlab.Client
}

func NewGitLabClient(project, baseURL, token string) (*GitLabClient, error) {
	c, err := gitlab.NewClient(project,
		gitlab.WithBaseURL(baseURL),
		gitlab.WithToken(token),
	)
	if err != nil {
		return nil, err
	}
	return &GitLabClient{Client: c}, nil
}

// PullRequest implements SCMClient.
func (c *GitLabClient) PullRequest(context.Context, int) (*PullRequest, error) {
	panic("unimplemented: PullRequest for GitLabClient")
}

// CreatePullRequest implements SCMClient.
func (c *GitLabClient) CreatePullRequest(_ context.Context, _, _, _, _ string) (*PullRequest, error) {
	panic("unimplemented: CreatePullRequest for GitLabClient")
}

// CopilotSession implements SCMClient.
func (c *GitLabClient) CopilotSession(context.Context, *TriggerContext) (string, error) {
	panic("unimplemented: CopilotSession for GitLabClient")
}

// CommentCopilot implements SCMClient.
func (c *GitLabClient) CommentCopilot(context.Context, int, *Copilot) error {
	panic("unimplemented: CommentCopilot for GitLabClient")
}

// CommentLint implements SCMClient.
func (c *GitLabClient) CommentLint(ctx context.Context, tc *TriggerContext, r *atlasexec.SummaryReport) error {
	comment, err := RenderTemplate("migrate-lint.tmpl", r, tc)
	if err != nil {
		return err
	}
	return c.upsertComment(ctx, tc.PullRequest, tc.Act.GetInput("dir-name"), comment)
}

// CommentPlan implements SCMClient.
func (c *GitLabClient) CommentPlan(ctx context.Context, tc *TriggerContext, p *atlasexec.SchemaPlan) error {
	// Report the schema plan to the user and add a comment to the PR.
	comment, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan": p,
	}, tc)
	if err != nil {
		return fmt.Errorf("failed to generate schema plan comment: %w", err)
	}
	return c.upsertComment(ctx, tc.PullRequest, p.File.Name, comment)
}

// CommentSchemaLint implements SCMClient.
func (c *GitLabClient) CommentSchemaLint(ctx context.Context, tc *TriggerContext, r *SchemaLintReport) error {
	id := schemaLintCommentID(tc)
	if len(r.Steps) == 0 {
		return c.deleteComment(ctx, tc.PullRequest, id)
	}
	comment, err := RenderTemplate("schema-lint.tmpl", r, tc)
	if err != nil {
		return err
	}
	return c.upsertComment(ctx, tc.PullRequest, id, comment)
}

func (c *GitLabClient) upsertComment(ctx context.Context, pr *PullRequest, id, comment string) error {
	return c.comment(ctx, pr, id, comment, true)
}

func (c *GitLabClient) deleteComment(ctx context.Context, pr *PullRequest, id string) error {
	if pr == nil {
		return fmt.Errorf("pull request is required for commenting")
	}
	comments, err := c.PullRequestNotes(ctx, pr.Number)
	if err != nil {
		return err
	}
	marker := commentMarker(id)
	if found := slices.IndexFunc(comments, func(c gitlab.Note) bool {
		return !c.System && strings.Contains(c.Body, marker)
	}); found != -1 {
		return c.DeleteNote(ctx, pr.Number, comments[found].ID)
	}
	return nil
}

func (c *GitLabClient) comment(ctx context.Context, pr *PullRequest, id, comment string, create bool) error {
	if pr == nil {
		return fmt.Errorf("pull request is required for commenting")
	}
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
	if !create {
		return nil
	}
	return c.CreateNote(ctx, pr.Number, comment)
}

var _ Action = (*GitLab)(nil)
var _ MigrateDriftReporter = (*GitLab)(nil)
var _ SCMClient = (*GitLabClient)(nil)
