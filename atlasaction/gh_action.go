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
	"slices"
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
		"EnvName":      a.GetInput("env"),
		"RerunCommand": fmt.Sprintf("gh run rerun %s", a.Getenv("GITHUB_RUN_ID")),
	})
	if err != nil {
		a.Errorf("failed to create summary: %v", err)
		return
	}
	a.AddStepSummary(summary)
}

// GetType implements the Action interface.
func (a *ghAction) GetType() atlasexec.TriggerType {
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

func (g *githubAPI) UpsertComment(ctx context.Context, pr *PullRequest, id, comment string) error {
	comments, err := g.getIssueComments(ctx, pr)
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
		return g.updateIssueComment(ctx, comments[found].ID, body)
	}
	return g.createIssueComment(ctx, pr, body)
}

func (g *githubAPI) getIssueComments(ctx context.Context, pr *PullRequest) ([]githubIssueComment, error) {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", g.repo, pr.Number, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", g.repo, pr.Number, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling GitHub API", res.StatusCode)
	}
	var comments []githubIssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", g.repo, pr.Number, string(buf), err)
	}
	return comments, nil
}

func (g *githubAPI) createIssueComment(ctx context.Context, pr *PullRequest, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, content)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
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
func (g *githubAPI) updateIssueComment(ctx context.Context, id int, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", g.baseURL, g.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, content)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
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

// UpsertSuggestion creates or updates a suggestion review comment on trigger event pull request.
func (g *githubAPI) UpsertSuggestion(ctx context.Context, pr *PullRequest, s *Suggestion) error {
	marker := commentMarker(s.ID)
	body := fmt.Sprintf("%s\n%s", s.Comment, marker)
	// TODO: Listing the comments only once and updating the comment in the same call.
	comments, err := g.listReviewComments(ctx, pr)
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
		if err := g.updateReviewComment(ctx, comments[found].ID, body); err != nil {
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
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
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
func (g *githubAPI) listReviewComments(ctx context.Context, pr *PullRequest) ([]pullRequestComment, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
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
func (g *githubAPI) updateReviewComment(ctx context.Context, id int, body string) error {
	type pullRequestUpdate struct {
		Body string `json:"body"`
	}
	b, err := json.Marshal(pullRequestUpdate{Body: body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/comments/%v", g.baseURL, g.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
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

// ListPullRequestFiles return paths of the files in the trigger event pull request.
func (g *githubAPI) ListPullRequestFiles(ctx context.Context, pr *PullRequest) ([]string, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/files", g.baseURL, g.repo, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
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
func (g *githubAPI) OpeningPullRequest(ctx context.Context, branch string) (*PullRequest, error) {
	owner, _, err := g.ownerRepo()
	if err != nil {
		return nil, err
	}
	// Get open pull requests for the branch.
	url := fmt.Sprintf("%s/repos/%s/pulls?state=open&head=%s:%s&sort=created&direction=desc&per_page=1&page=1",
		g.baseURL, g.repo, owner, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
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

func (g *githubAPI) ownerRepo() (string, string, error) {
	s := strings.Split(g.repo, "/")
	if len(s) != 2 {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY must be in the format of 'owner/repo'")
	}
	return s[0], s[1], nil
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
