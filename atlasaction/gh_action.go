// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/mitchellh/mapstructure"
	"github.com/sethvargo/go-githubactions"
)

// ghAction is an implementation of the Action interface for GitHub Actions.
type ghAction struct {
	*githubactions.Action
}

// NewGHAction returns a new Action for GitHub Actions.
func NewGHAction(getenv func(string) string, w io.Writer) *ghAction {
	return &ghAction{
		githubactions.New(
			githubactions.WithGetenv(getenv),
			githubactions.WithWriter(w),
		),
	}
}

// MigrateApply implements Reporter.
func (a *ghAction) MigrateApply(_ context.Context, r *atlasexec.MigrateApply) {
	summary, err := RenderTemplate("migrate-apply.tmpl", r)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// MigrateLint implements Reporter.
func (a *ghAction) MigrateLint(_ context.Context, r *atlasexec.SummaryReport) {
	if err := a.addChecks(r); err != nil {
		a.Errorf("failed to add checks: %v", err)
	}
	summary, err := RenderTemplate("migrate-lint.tmpl", r)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// SchemaApply implements Reporter.
func (a *ghAction) SchemaApply(_ context.Context, r *atlasexec.SchemaApply) {
	summary, err := RenderTemplate("schema-apply.tmpl", r)
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// SchemaPlan implements Reporter.
func (a *ghAction) SchemaPlan(_ context.Context, r *atlasexec.SchemaPlan) {
	summary, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan":         r,
		"RerunCommand": fmt.Sprintf("gh run rerun %s", a.Getenv("GITHUB_RUN_ID")),
	})
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// GetType implements the Action interface.
func (*ghAction) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGithubAction
}

// GetTriggerContext returns the context of the action.
func (a *ghAction) GetTriggerContext(context.Context) (*TriggerContext, error) {
	ctx, err := a.Action.Context()
	if err != nil {
		return nil, err
	}
	ev, err := extractEvent(ctx.Event)
	if err != nil {
		return nil, err
	}
	tc := &TriggerContext{
		Act:      a,
		SCM:      SCM{Type: atlasexec.SCMTypeGithub, APIURL: ctx.APIURL},
		Repo:     ctx.Repository,
		Branch:   ctx.HeadRef,
		Commit:   ctx.SHA,
		RepoURL:  ev.Repository.URL,
		Actor:    &Actor{Name: ctx.Actor, ID: ctx.ActorID},
		RerunCmd: fmt.Sprintf("gh run rerun %d", ctx.RunID),
	}
	if tc.Branch == "" {
		// HeadRef will be empty for push events, so we use RefName instead.
		tc.Branch = ctx.RefName
	}
	if ctx.EventName == "pull_request" {
		tc.PullRequest = &PullRequest{
			Number: ev.PullRequest.Number,
			URL:    ev.PullRequest.URL,
			Commit: ev.PullRequest.Head.SHA,
			Body:   ev.PullRequest.Body,
		}
	}
	return tc, nil
}

// WithFieldsMap return a new Logger with the given fields.
func (a *ghAction) WithFieldsMap(m map[string]string) Logger {
	return &ghAction{a.Action.WithFieldsMap(m)}
}

// addChecks runs annotations to the trigger event pull request for the given payload.
func (a *ghAction) addChecks(lint *atlasexec.SummaryReport) error {
	// Get the directory path from the lint report.
	dir := path.Join(a.GetInput("working-directory"), lint.Env.Dir)
	for _, file := range lint.Files {
		filePath := path.Join(dir, file.Name)
		if file.Error != "" && len(file.Reports) == 0 {
			a.WithFieldsMap(map[string]string{
				"file": filePath,
				"line": "1",
			}).Errorf(file.Error)
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
					logger.Errorf(msg)
				} else {
					logger.Warningf(msg)
				}
			}
		}
	}
	return nil
}

var _ Action = (*ghAction)(nil)
var _ Reporter = (*ghAction)(nil)

const defaultGHApiUrl = "https://api.github.com"

// githubClient returns a new GitHub client for the given repository.
// If the GITHUB_TOKEN is set, it will be used for authentication.
func githubClient(repo, baseURL string, token string) *githubAPI {
	if baseURL == "" {
		baseURL = defaultGHApiUrl
	}
	httpClient := &http.Client{Timeout: time.Second * 30}
	if token != "" {
		httpClient.Transport = &oauth2.Transport{
			Base: http.DefaultTransport,
			Source: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: token,
			}),
		}
	}
	return &githubAPI{
		baseURL: baseURL,
		repo:    repo,
		client:  httpClient,
	}
}

type (
	// ghAPI is an implementation of the SCMClient interface for GitHub Actions.
	githubAPI struct {
		baseURL string
		repo    string
		client  *http.Client
	}
	githubIssueComment struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}
	pullRequestComment struct {
		ID        int    `json:"id,omitempty"`
		Body      string `json:"body"`
		Path      string `json:"path"`
		CommitID  string `json:"commit_id,omitempty"`
		StartLine int    `json:"start_line,omitempty"`
		Line      int    `json:"line,omitempty"`
	}
	pullRequestFile struct {
		Name string `json:"filename"`
	}
)

// CommentLint implements SCMClient.
func (c *githubAPI) CommentLint(ctx context.Context, tc *TriggerContext, r *atlasexec.SummaryReport) error {
	comment, err := RenderTemplate("migrate-lint.tmpl", r)
	if err != nil {
		return err
	}
	err = c.comment(ctx, tc.PullRequest, tc.Act.GetInput("dir-name"), comment)
	if err != nil {
		return err
	}
	switch files, err := c.listPullRequestFiles(ctx, tc.PullRequest); {
	case err != nil:
		tc.Act.Errorf("failed to list pull request files: %w", err)
	default:
		err = addSuggestions(tc.Act, r, func(s *Suggestion) error {
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
func (c *githubAPI) CommentPlan(ctx context.Context, tc *TriggerContext, p *atlasexec.SchemaPlan) error {
	// Report the schema plan to the user and add a comment to the PR.
	comment, err := RenderTemplate("schema-plan.tmpl", map[string]any{
		"Plan": p,
	})
	if err != nil {
		return err
	}
	return c.comment(ctx, tc.PullRequest, p.File.Name, comment)
}

func (c *githubAPI) comment(ctx context.Context, pr *PullRequest, id, comment string) error {
	comments, err := c.getIssueComments(ctx, pr)
	if err != nil {
		return err
	}
	var (
		marker = commentMarker(id)
		body   = strings.NewReader(fmt.Sprintf(`{"body": %q}`, comment+"\n"+marker))
	)
	if found := slices.IndexFunc(comments, func(c githubIssueComment) bool {
		return strings.Contains(c.Body, marker)
	}); found != -1 {
		return c.updateIssueComment(ctx, comments[found].ID, body)
	}
	return c.createIssueComment(ctx, pr, body)
}

func (c *githubAPI) getIssueComments(ctx context.Context, pr *PullRequest) ([]githubIssueComment, error) {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", c.baseURL, c.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", c.repo, pr.Number, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", c.repo, pr.Number, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling GitHub API", res.StatusCode)
	}
	var comments []githubIssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", c.repo, pr.Number, string(buf), err)
	}
	return comments, nil
}

func (c *githubAPI) createIssueComment(ctx context.Context, pr *PullRequest, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", c.baseURL, c.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, content)
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// updateIssueComment updates issue comment with the given id.
func (c *githubAPI) updateIssueComment(ctx context.Context, id int, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", c.baseURL, c.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, content)
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// upsertSuggestion creates or updates a suggestion review comment on trigger event pull request.
func (c *githubAPI) upsertSuggestion(ctx context.Context, pr *PullRequest, s *Suggestion) error {
	marker := commentMarker(s.ID)
	body := fmt.Sprintf("%s\n%s", s.Comment, marker)
	// TODO: Listing the comments only once and updating the comment in the same call.
	comments, err := c.listReviewComments(ctx, pr)
	if err != nil {
		return err
	}
	// Search for the comment marker in the comments list.
	// If found, update the comment with the new suggestion.
	// If not found, create a new suggestion comment.
	found := slices.IndexFunc(comments, func(c pullRequestComment) bool {
		return c.Path == s.Path && strings.Contains(c.Body, marker)
	})
	if found != -1 {
		if err := c.updateReviewComment(ctx, comments[found].ID, body); err != nil {
			return err
		}
		return nil
	}
	buf, err := json.Marshal(pullRequestComment{
		Body:      body,
		Path:      s.Path,
		CommitID:  pr.Commit,
		Line:      s.Line,
		StartLine: s.StartLine,
	})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", c.baseURL, c.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// listReviewComments for the trigger event pull request.
func (c *githubAPI) listReviewComments(ctx context.Context, pr *PullRequest) ([]pullRequestComment, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", c.baseURL, c.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var comments []pullRequestComment
	if err = json.NewDecoder(res.Body).Decode(&comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// updateReviewComment updates the review comment with the given id.
func (c *githubAPI) updateReviewComment(ctx context.Context, id int, body string) error {
	type pullRequestUpdate struct {
		Body string `json:"body"`
	}
	b, err := json.Marshal(pullRequestUpdate{Body: body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/comments/%v", c.baseURL, c.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return err
}

// listPullRequestFiles return paths of the files in the trigger event pull request.
func (c *githubAPI) listPullRequestFiles(ctx context.Context, pr *PullRequest) ([]string, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/files", c.baseURL, c.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var files []pullRequestFile
	if err = json.NewDecoder(res.Body).Decode(&files); err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Name
	}
	return paths, nil
}

// OpeningPullRequest returns the latest open pull request for the given branch.
func (c *githubAPI) OpeningPullRequest(ctx context.Context, branch string) (*PullRequest, error) {
	owner, _, err := c.ownerRepo()
	if err != nil {
		return nil, err
	}
	// Get open pull requests for the branch.
	url := fmt.Sprintf("%s/repos/%s/pulls?state=open&head=%s:%s&sort=created&direction=desc&per_page=1&page=1",
		c.baseURL, c.repo, owner, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	switch buf, err := io.ReadAll(res.Body); {
	case err != nil:
		return nil, fmt.Errorf("error reading open pull requests: %w", err)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("unexpected status code: %d when calling GitHub API", res.StatusCode)
	default:
		var resp []struct {
			Url    string `json:"url"`
			Number int    `json:"number"`
			Head   struct {
				Sha string `json:"sha"`
			} `json:"head"`
		}
		if err = json.Unmarshal(buf, &resp); err != nil {
			return nil, err
		}
		if len(resp) == 0 {
			return nil, nil
		}
		return &PullRequest{
			Number: resp[0].Number,
			URL:    resp[0].Url,
			Commit: resp[0].Head.Sha,
		}, nil
	}
}

func (c *githubAPI) ownerRepo() (string, string, error) {
	s := strings.Split(c.repo, "/")
	if len(s) != 2 {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY must be in the format of 'owner/repo'")
	}
	return s[0], s[1], nil
}

type Suggestion struct {
	ID        string // Unique identifier for the suggestion.
	Path      string // File path.
	StartLine int    // Start line numbers for the suggestion.
	Line      int    // End line number for the suggestion.
	Comment   string // Comment body.
}

// addSuggestions returns the suggestions from the lint report.
func addSuggestions(a Action, lint *atlasexec.SummaryReport, fn func(*Suggestion) error) (err error) {
	if !slices.ContainsFunc(lint.Files, func(f *atlasexec.FileReport) bool {
		return len(f.Reports) > 0
	}) {
		// No reports to add suggestions.
		return nil
	}
	dir := a.GetInput("working-directory")
	for _, file := range lint.Files {
		filePath := path.Join(dir, lint.Env.Dir, file.Name)
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
				})
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
					})
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

// githubTriggerEvent is the structure of the GitHub trigger event.
type githubTriggerEvent struct {
	PullRequest struct {
		Number int    `mapstructure:"number"`
		Body   string `mapstructure:"body"`
		URL    string `mapstructure:"html_url"`
		Head   struct {
			SHA string `mapstructure:"sha"`
		} `mapstructure:"head"`
	} `mapstructure:"pull_request"`
	Repository struct {
		URL string `mapstructure:"html_url"`
	} `mapstructure:"repository"`
}

// extractEvent extracts the trigger event data from the raw event.
func extractEvent(raw map[string]any) (*githubTriggerEvent, error) {
	var event githubTriggerEvent
	if err := mapstructure.Decode(raw, &event); err != nil {
		return nil, fmt.Errorf("failed to parse push event: %v", err)
	}
	return &event, nil
}
