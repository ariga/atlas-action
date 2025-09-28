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
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ariga.io/atlas-action/internal/github"
	"ariga.io/atlas/atlasexec"
	"github.com/sethvargo/go-githubactions"
	"golang.org/x/oauth2"
)

// GitHub is an implementation of the Action interface for GitHub Actions.
type GitHub struct {
	*githubactions.Action
}

// NewGitHub returns a new Action for GitHub Actions.
func NewGitHub(getenv func(string) string, w io.Writer) *GitHub {
	return &GitHub{
		githubactions.New(
			githubactions.WithGetenv(getenv),
			githubactions.WithWriter(w),
		),
	}
}

// MigrateApply implements Reporter.
func (a *GitHub) MigrateApply(_ context.Context, r *atlasexec.MigrateApply) {
	summary, err := RenderTemplate("migrate-apply.tmpl", r, nil)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// MigrateLint implements Reporter.
func (a *GitHub) MigrateLint(_ context.Context, r *atlasexec.SummaryReport) {
	if err := a.addChecks(r); err != nil {
		a.Errorf("failed to add checks: %v", err)
	}
	summary, err := RenderTemplate("migrate-lint.tmpl", r, nil)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// SchemaApply implements Reporter.
func (a *GitHub) SchemaApply(_ context.Context, r *atlasexec.SchemaApply) {
	summary, err := RenderTemplate("schema-apply.tmpl", r, nil)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// SchemaPlan implements Reporter.
func (a *GitHub) SchemaPlan(_ context.Context, r *atlasexec.SchemaPlan) {
	summary, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan":         r,
		"RerunCommand": fmt.Sprintf("gh run rerun %s", a.Getenv("GITHUB_RUN_ID")),
	}, nil)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// SchemaLint implements Reporter.
func (a *GitHub) SchemaLint(_ context.Context, r *SchemaLintReport) {
	if err := a.addChecksSchemaLint(r); err != nil {
		a.Errorf("failed to add checks: %v", err)
	}
	summary, err := RenderTemplate("schema-lint.tmpl", r, nil)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// GetType implements the Action interface.
func (*GitHub) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGithubAction
}

// GetTriggerContext returns the context of the action.
func (a *GitHub) GetTriggerContext(context.Context) (*TriggerContext, error) {
	ctx, err := a.Action.Context()
	if err != nil {
		return nil, err
	}
	ev, err := github.ExtractEvent(ctx.Event)
	if err != nil {
		return nil, err
	}
	tc := &TriggerContext{
		Act:     a,
		SCMType: atlasexec.SCMTypeGithub,
		SCMClient: func() (SCMClient, error) {
			token := a.Getenv("GITHUB_TOKEN")
			if token == "" {
				a.Warningf("GITHUB_TOKEN is not set, the action may not have all the permissions")
				if os.Getenv("GITHUB_ACTIONS") != "" {
					a.Warningf("On GitHub Actions, you can set the token in the workflow file:")
					a.Warningf("  ```yaml")
					a.Warningf("  env:")
					a.Warningf("    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}")
					a.Warningf("  permissions:")
					a.Warningf("    contents: read")
					a.Warningf("    pull-requests: write")
					a.Warningf("  ```")
					a.Warningf("  You can also set a personal access token with the required permissions:")
					a.Warningf("  ```yaml")
					a.Warningf("  env:")
					a.Warningf("    GITHUB_TOKEN: ${{ secrets.PERSONAL_ACCESS_TOKEN }}")
					a.Warningf("  ```")
				}
			}
			return NewGitHubClient(ctx.Repository, ctx.APIURL, token)
		},
		Repo:          ctx.Repository,
		Branch:        ctx.HeadRef,
		Commit:        ctx.SHA,
		RepoURL:       ev.Repository.URL,
		DefaultBranch: ev.Repository.DefaultBranch,
		Actor:         &Actor{Name: ctx.Actor, ID: ctx.ActorID},
		RerunCmd:      fmt.Sprintf("gh run rerun %d", ctx.RunID),
	}
	if tc.Branch == "" {
		// HeadRef will be empty for push events, so we use RefName instead.
		tc.Branch = ctx.RefName
	}
	switch {
	case ctx.EventName == "pull_request":
		tc.PullRequest = &PullRequest{
			Number: ev.PullRequest.Number,
			URL:    ev.PullRequest.URL,
			Commit: ev.PullRequest.Head.SHA,
			Body:   ev.PullRequest.Body,
		}
	case ctx.EventName == "issue_comment" && ev.Issue.PullRequest != nil:
		tc.Comment = &Comment{
			Number: ev.Issue.Number,
			URL:    ev.Comment.URL,
			Body:   ev.Comment.Body,
		}
	}
	return tc, nil
}

// addChecks runs annotations to the trigger event pull request for the given payload.
func (a *GitHub) addChecks(lint *atlasexec.SummaryReport) error {
	// Get the directory path from the lint report.
	dir := filepath.Join(a.GetInput("working-directory"), lint.Env.Dir)
	for _, file := range lint.Files {
		filePath := filepath.Join(dir, file.Name)
		if file.Error != "" && len(file.Reports) == 0 {
			a.WithFieldsMap(map[string]string{
				"file": filePath,
				"line": "1",
			}).Errorf("%s", file.Error)
			continue
		}
		for _, report := range file.Reports {
			for _, diag := range report.Diagnostics {
				msg := diag.Text
				if diag.Code != "" {
					msg = fmt.Sprintf("%v (%v)\n\nDetails: https://atlasgo.io/lint/analyzers#%v", msg, diag.Code, diag.Code)
				}
				lines := strings.Split(file.Text[:diag.Pos], "\n")
				logger := a.WithFieldsMap(map[string]string{
					"file":  filePath,
					"line":  strconv.Itoa(max(1, len(lines))),
					"title": report.Text,
				})
				if file.Error != "" {
					logger.Errorf("%s", msg)
				} else {
					logger.Warningf("%s", msg)
				}
			}
		}
	}
	return nil
}

// addChecksSchemaLint runs annotations to the trigger event pull request for the given schema lint report.
func (a *GitHub) addChecksSchemaLint(lint *SchemaLintReport) error {
	for _, step := range lint.Steps {
		for _, diag := range step.Diagnostics {
			msg := diag.Text
			if diag.Code != "" {
				msg = fmt.Sprintf("%v (%v)\n\nDetails: https://atlasgo.io/lint/analyzers#%v", msg, diag.Code, diag.Code)
			}
			logger := a.WithFieldsMap(map[string]string{
				"title": step.Text,
			})
			if diag.Pos != nil {
				file := diag.Pos.Filename
				if !filepath.IsAbs(file) {
					// If the file is not absolute, we assume it is relative to the working directory.
					file = filepath.Join(a.GetInput("working-directory"), file)
				}
				logger = logger.WithFieldsMap(map[string]string{
					"file": file,
					"line": strconv.Itoa(max(1, diag.Pos.Start.Line)),
				})
			}
			if step.Error {
				logger.Errorf("%s", msg)
			} else {
				logger.Warningf("%s", msg)
			}
		}
	}
	return nil
}

type GitHubClient struct {
	*github.Client
}

func NewGitHubClient(repo, baseURL, token string) (*GitHubClient, error) {
	c, err := github.NewClient(repo,
		github.WithBaseURL(baseURL),
		github.WithToken(&oauth2.Token{AccessToken: token}),
	)
	if err != nil {
		return nil, err
	}
	return &GitHubClient{Client: c}, nil
}

// PullRequest implements SCMClient.
func (c *GitHubClient) PullRequest(ctx context.Context, number int) (*PullRequest, error) {
	pr, err := c.Client.PullRequest(ctx, number)
	if err != nil {
		return nil, err
	}
	return convertPullRequest(pr), nil
}

// CreatePullRequest implements SCMClient.
func (c *GitHubClient) CreatePullRequest(ctx context.Context, head, base, title, body string) (*PullRequest, error) {
	pr, err := c.Client.CreatePullRequest(ctx, head, base, title, body)
	if err != nil {
		return nil, err
	}
	return convertPullRequest(pr), nil
}

const copilotSession = "%s\n\n<!-- generated by ariga/atlas-action/copilot for session %s -->"

var reCopilotSession = regexp.MustCompile(`<!-- generated by ariga/atlas-action/copilot for session (.*) -->`)

// CopilotSession implements SCMClient.
func (c *GitHubClient) CopilotSession(ctx context.Context, tc *TriggerContext) (string, error) {
	cs, err := c.IssueComments(ctx, func() int {
		if tc.PullRequest != nil {
			return tc.PullRequest.Number
		}
		return tc.Comment.Number
	}())
	if err != nil {
		return "", err
	}
	for _, c := range cs {
		if m := reCopilotSession.FindStringSubmatch(c.Body); len(m) > 1 {
			return m[1], nil
		}
	}
	return "", nil
}

// CommentCopilot implements SCMClient.
func (c *GitHubClient) CommentCopilot(ctx context.Context, pr int, cp *Copilot) error {
	var buf strings.Builder
	if cp.Prompt != "" {
		fmt.Fprintf(&buf, "> %s\n\n", cp.Prompt)
	}
	fmt.Fprintf(&buf, copilotSession, cp.Response, cp.Session)
	return c.CreateIssueComment(ctx, pr, buf.String())
}

// CommentLint implements SCMClient.
func (c *GitHubClient) CommentLint(ctx context.Context, tc *TriggerContext, r *atlasexec.SummaryReport) error {
	comment, err := RenderTemplate("migrate-lint.tmpl", r, tc)
	if err != nil {
		return err
	}
	err = c.upsertComment(ctx, tc.PullRequest, tc.Act.GetInput("dir-name"), comment)
	if err != nil {
		return err
	}
	switch files, err := c.ListPullRequestFiles(ctx, tc.PullRequest.Number); {
	case err != nil:
		tc.Act.Errorf("failed to list pull request files: %w", err)
	default:
		err = addSuggestions(tc.Act.GetInput("working-directory"), r, func(s *Suggestion) error {
			// Add suggestion only if the file is part of the pull request.
			if slices.Contains(files, s.Path) {
				return c.upsertSuggestion(ctx, tc.PullRequest, s)
			}
			return nil
		})
		if err != nil {
			tc.Act.Errorf("failed to add suggestion on the pull request: %v", err)
		}
	}
	return nil
}

// CommentPlan implements SCMClient.
func (c *GitHubClient) CommentPlan(ctx context.Context, tc *TriggerContext, p *atlasexec.SchemaPlan) error {
	// Report the schema plan to the user and add a comment to the PR.
	comment, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan":         p,
		"RerunCommand": tc.RerunCmd,
	}, tc)
	if err != nil {
		return err
	}
	return c.upsertComment(ctx, tc.PullRequest, p.File.Name, comment)
}

// CommentSchemaLint implements SCMClient.
func (c *GitHubClient) CommentSchemaLint(ctx context.Context, tc *TriggerContext, r *SchemaLintReport) error {
	comment, err := RenderTemplate("schema-lint.tmpl", r, tc)
	if err != nil {
		return err
	}
	id := "schema-lint"
	if url := tc.Act.GetInput("url"); url != "" {
		id = url
	}
	return c.upsertComment(ctx, tc.PullRequest, id, comment)
}

func (c *GitHubClient) upsertComment(ctx context.Context, pr *PullRequest, id, comment string) error {
	comments, err := c.IssueComments(ctx, pr.Number)
	if err != nil {
		return err
	}
	marker := commentMarker(id)
	comment += "\n" + marker
	if found := slices.IndexFunc(comments, func(c github.IssueComment) bool {
		return strings.Contains(c.Body, marker)
	}); found != -1 {
		err = c.UpdateIssueComment(ctx, comments[found].ID, comment)
	} else {
		err = c.CreateIssueComment(ctx, pr.Number, comment)
	}
	return err
}

func (c *GitHubClient) upsertSuggestion(ctx context.Context, pr *PullRequest, s *Suggestion) error {
	// TODO: Listing the comments only once and updating the comment in the same call.
	comments, err := c.ReviewComments(ctx, pr.Number)
	if err != nil {
		return err
	}
	var (
		marker  = commentMarker(s.ID)
		comment = s.Comment + "\n" + marker
	)
	if found := slices.IndexFunc(comments, func(c github.PullRequestComment) bool {
		return c.Path == s.Path && strings.Contains(c.Body, marker)
	}); found != -1 {
		err = c.UpdateReviewComment(ctx, comments[found].ID, comment)
	} else {
		err = c.CreateReviewComment(ctx, pr.Number, &github.PullRequestComment{
			CommitID:  pr.Commit,
			Body:      comment,
			Path:      s.Path,
			Line:      s.Line,
			StartLine: s.StartLine,
		})
	}
	return err
}

type Suggestion struct {
	ID        string // Suggestion ID.
	Path      string // File path.
	StartLine int    // Start line numbers for the suggestion.
	Line      int    // End line number for the suggestion.
	Comment   string // Comment body.
}

// addSuggestions returns the suggestions from the lint report.
func addSuggestions(cw string, lint *atlasexec.SummaryReport, fn func(*Suggestion) error) (err error) {
	if !slices.ContainsFunc(lint.Files, func(f *atlasexec.FileReport) bool {
		return len(f.Reports) > 0
	}) {
		// No reports to add suggestions.
		return nil
	}
	for _, file := range lint.Files {
		filePath := filepath.Join(cw, lint.Env.Dir, file.Name)
		for reportIdx, report := range file.Reports {
			for _, f := range report.SuggestedFixes {
				if f.TextEdit == nil {
					continue
				}
				s := Suggestion{Path: filePath, ID: f.Message}
				if f.TextEdit.End <= f.TextEdit.Line {
					s.Line = f.TextEdit.Line
				} else {
					s.StartLine = f.TextEdit.Line
					s.Line = f.TextEdit.End
				}
				s.Comment, err = RenderTemplate("suggestion.tmpl", map[string]any{
					"Fix": f,
					"Dir": lint.Env.Dir,
				}, nil)
				if err != nil {
					return fmt.Errorf("failed to render suggestion: %w", err)
				}
				if err = fn(&s); err != nil {
					return fmt.Errorf("failed to process suggestion: %w", err)
				}
			}
			for diagIdx, d := range report.Diagnostics {
				for _, f := range d.SuggestedFixes {
					if f.TextEdit == nil {
						continue
					}
					s := Suggestion{Path: filePath, ID: f.Message}
					if f.TextEdit.End <= f.TextEdit.Line {
						s.Line = f.TextEdit.Line
					} else {
						s.StartLine = f.TextEdit.Line
						s.Line = f.TextEdit.End
					}
					s.Comment, err = RenderTemplate("suggestion.tmpl", map[string]any{
						"Fix":    f,
						"Dir":    lint.Env.Dir,
						"File":   file,
						"Report": reportIdx,
						"Diag":   diagIdx,
					}, nil)
					if err != nil {
						return fmt.Errorf("failed to render suggestion: %w", err)
					}
					if err = fn(&s); err != nil {
						return fmt.Errorf("failed to process suggestion: %w", err)
					}
				}
			}
		}
	}
	return nil
}

func convertPullRequest(pr *github.PullRequest) *PullRequest {
	if pr == nil {
		return nil
	}
	return &PullRequest{
		Number: pr.Number,
		URL:    pr.URL,
		Body:   pr.Body,
		Commit: pr.Commit,
		Ref:    pr.Ref,
	}
}

var _ Action = (*GitHub)(nil)
var _ Reporter = (*GitHub)(nil)
var _ SCMClient = (*GitHubClient)(nil)
