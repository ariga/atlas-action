// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
	"ariga.io/atlas/sql/sqlcheck"
	"github.com/mitchellh/mapstructure"
	"github.com/sethvargo/go-githubactions"
)

// version holds atlas-action version. When built with cloud packages should be set by build flag, e.g.
// "-X 'ariga.io/atlas-action/atlasaction.Version=v0.1.2'"
var Version string

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func MigrateApply(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	dryRun, err := func() (bool, error) {
		inp := act.GetInput("dry-run")
		if inp == "" {
			return false, nil
		}
		return strconv.ParseBool(inp)
	}()
	if err != nil {
		return fmt.Errorf(`invlid value for the "dry-run" input: %w`, err)
	}
	params := &atlasexec.MigrateApplyParams{
		URL:             act.GetInput("url"),
		DirURL:          act.GetInput("dir"),
		ConfigURL:       act.GetInput("config"),
		Env:             act.GetInput("env"),
		DryRun:          dryRun,
		TxMode:          act.GetInput("tx-mode"),  // Hidden param.
		BaselineVersion: act.GetInput("baseline"), // Hidden param.
		Context: &atlasexec.DeployRunContext{
			TriggerType:    atlasexec.TriggerTypeGithubAction,
			TriggerVersion: Version,
		},
	}
	run, err := client.MigrateApply(ctx, params)
	if err != nil {
		act.SetOutput("error", err.Error())
		return err
	}
	if run.Error != "" {
		act.SetOutput("error", run.Error)
		return errors.New(run.Error)
	}
	act.SetOutput("current", run.Current)
	act.SetOutput("target", run.Target)
	act.SetOutput("pending_count", strconv.Itoa(len(run.Pending)))
	act.SetOutput("applied_count", strconv.Itoa(len(run.Applied)))
	return nil
}

// MigratePush runs the GitHub Action for "ariga/atlas-action/migrate/push"
func MigratePush(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	runContext, err := createRunContext(act)
	if err != nil {
		return fmt.Errorf("failed to read github metadata: %w", err)
	}
	params := &atlasexec.MigratePushParams{
		Name:      act.GetInput("dir-name"),
		DirURL:    act.GetInput("dir"),
		DevURL:    act.GetInput("dev-url"),
		Context:   runContext,
		ConfigURL: act.GetInput("config"),
		Env:       act.GetInput("env"),
	}
	_, err = client.MigratePush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push directory: %v", err)
	}
	tag := act.GetInput("tag")
	params.Tag = runContext.Commit
	if tag != "" {
		params.Tag = tag
	}
	resp, err := client.MigratePush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push dir tag: %w", err)
	}
	act.SetOutput("url", resp)
	act.Infof("Uploaded dir %q to Atlas Cloud", params.Name)
	return nil
}

// MigrateLint runs the GitHub Action for "ariga/atlas-action/migrate/lint"
func MigrateLint(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	if act.GetInput("dir-name") == "" {
		return errors.New("atlasaction: missing required parameter dir-name")
	}
	runContext, err := createRunContext(act)
	if err != nil {
		return fmt.Errorf("failed to read github metadata: %w", err)
	}
	var (
		resp    bytes.Buffer
		payload atlasexec.SummaryReport
	)
	err = client.MigrateLintError(ctx, &atlasexec.MigrateLintParams{
		DevURL:    act.GetInput("dev-url"),
		DirURL:    act.GetInput("dir"),
		ConfigURL: act.GetInput("config"),
		Env:       act.GetInput("env"),
		Base:      "atlas://" + act.GetInput("dir-name"),
		Context:   runContext,
		Web:       true,
		Writer:    &resp,
	})
	isLintErr := err != nil && errors.Is(err, atlasexec.LintErr)
	if err != nil && !isLintErr {
		return err
	}
	if err := json.NewDecoder(&resp).Decode(&payload); err != nil {
		return fmt.Errorf("decoding payload: %w", err)
	}
	if payload.URL != "" {
		act.SetOutput("report-url", payload.URL)
	}
	ghContext, err := act.Context()
	if err != nil {
		return err
	}
	event, err := triggerEvent(ghContext)
	if err != nil {
		return err
	}
	ghClient := githubAPI{
		event:   event,
		baseURL: ghContext.APIURL,
		repo:    ghContext.Repository,
		client: &http.Client{
			Transport: &roundTripper{
				authToken: act.Getenv("GITHUB_TOKEN"),
			},
			Timeout: time.Second * 30,
		},
	}
	var summary bytes.Buffer
	if err := comment.Execute(&summary, &payload); err != nil {
		return err
	}
	if err := ghClient.addSummary(act, summary.String()); err != nil {
		return err
	}
	if err := ghClient.addChecks(act, &payload); err != nil {
		return err
	}
	if err := ghClient.addSuggestions(act, &payload); err != nil {
		return err
	}
	if isLintErr {
		return fmt.Errorf("lint completed with errors, see report: %s", payload.URL)
	}
	return nil
}

func fileErrors(s *atlasexec.SummaryReport) int {
	count := 0
	for _, f := range s.Files {
		if len(f.Error) > 0 {
			count++
		}
	}
	return count
}

var (
	//go:embed comment.tmpl
	commentTmpl string
	comment     = template.Must(
		template.New("comment").
			Funcs(template.FuncMap{
				"fileErrors": fileErrors,
			}).
			Parse(commentTmpl),
	)
)

// addSummary writes a summary to the pull request. It adds a marker
// HTML comment to the end of the comment body to identify the comment as one created by
// this action.
func (g *githubAPI) addSummary(act *githubactions.Action, summary string) error {
	act.AddStepSummary(summary)
	prNumber := g.event.PullRequest.Number
	if prNumber == 0 {
		return nil
	}
	comments, err := g.getIssueComments()
	if err != nil {
		return err
	}
	marker := commentMarker(act.GetInput("dir-name"))
	comment := struct {
		Body string `json:"body"`
	}{
		Body: summary + "\n" + marker,
	}
	buf, err := json.Marshal(comment)
	if err != nil {
		return err
	}
	r := bytes.NewReader(buf)
	found := slices.IndexFunc(comments, func(c githubIssueComment) bool {
		return strings.Contains(c.Body, marker)
	})
	if found != -1 {
		return g.updateIssueComment(comments[found].ID, r)
	}
	return g.createIssueComment(r)
}

// addChecks runs annotations to the trigger event pull request for the given payload.
func (g *githubAPI) addChecks(act *githubactions.Action, payload *atlasexec.SummaryReport) error {
	dir := payload.Env.Dir
	for _, file := range payload.Files {
		filePath := path.Join(dir, file.Name)
		if file.Error != "" && len(file.Reports) == 0 {
			act.WithFieldsMap(map[string]string{
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
				act = act.WithFieldsMap(map[string]string{
					"file":  filePath,
					"line":  strconv.Itoa(max(1, len(lines))),
					"title": report.Text,
				})
				if file.Error != "" {
					act.Errorf(msg)
				} else {
					act.Warningf(msg)
				}
			}
		}
	}
	return nil
}

// addSuggestions comments on the trigger event pull request for the given payload.
func (g *githubAPI) addSuggestions(act *githubactions.Action, payload *atlasexec.SummaryReport) error {
	hasReport := false
	for _, f := range payload.Files {
		if len(f.Reports) > 0 {
			hasReport = true
			break
		}
	}
	if !hasReport {
		return nil
	}
	changedFiles, err := g.listPullRequestFiles()
	if err != nil {
		return err
	}
	for _, file := range payload.Files {
		// Sending suggestions only for the files that are part of the PR.
		filePath := path.Join(payload.Env.Dir, file.Name)
		if !slices.Contains(changedFiles, filePath) {
			continue
		}
		for _, report := range file.Reports {
			for _, s := range report.SuggestedFixes {
				body := fmt.Sprintf("%s\n```suggestion\n%s\n```", s.Message, s.TextEdit.NewText)
				if err := g.upsertSuggestion(filePath, body, s); err != nil {
					return err
				}
			}
			for _, d := range report.Diagnostics {
				for _, s := range d.SuggestedFixes {
					sevirity := "WARNING"
					if file.Error != "" {
						sevirity = "CAUTION"
					}
					title := fmt.Sprintf("> [!%s]\n" +
						"> **%s**\n" +
						"> %s", sevirity, report.Text, d.Text)
					if d.Code != "" {
						title = fmt.Sprintf("%v [%v](https://atlasgo.io/lint/analyzers#%v)\n", title, d.Code, d.Code)
					}
					body:= fmt.Sprintf("%s\n%s\n```suggestion\n%s\n```", title, s.Message, s.TextEdit.NewText)
					if err := g.upsertSuggestion(filePath, body, s); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

type (
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

	githubAPI struct {
		event   *githubTriggerEvent
		baseURL string
		repo    string
		client  *http.Client
	}

	// roundTripper is a http.RoundTripper that adds the Authorization header.
	roundTripper struct {
		authToken string
	}
)

// RoundTrip implements http.RoundTripper.
func (r *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Accept", "application/vnd.github+json")
	req.Header.Add("Authorization", "Bearer "+r.authToken)
	req.Header.Add("X-GitHub-Api-Version", "2022-11-28")
	return http.DefaultTransport.RoundTrip(req)
}

func (g *githubAPI) getIssueComments() ([]githubIssueComment, error) {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, g.repo, g.event.PullRequest.Number)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", g.repo, g.event.PullRequest.Number, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", g.repo, g.event.PullRequest.Number, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling GitHub API", res.StatusCode)
	}
	var comments []githubIssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", g.repo, g.event.PullRequest.Number, string(buf), err)
	}
	return comments, nil
}

func (g *githubAPI) createIssueComment(content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, g.repo, g.event.PullRequest.Number)
	req, err := http.NewRequest(http.MethodPost, url, content)
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
func (g *githubAPI) updateIssueComment(id int, content io.Reader) error {
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", g.baseURL, g.repo, id)
	req, err := http.NewRequest(http.MethodPatch, url, content)
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

// upsertSuggestion creates or updates a suggestion review comment on trigger event pull request.
func (g *githubAPI) upsertSuggestion(filePath, body string, suggestion sqlcheck.SuggestedFix) error {
	marker := commentMarker(suggestion.Message)
	body = fmt.Sprintf("%s\n%s", body, marker)
	comments, err := g.listReviewComments()
	if err != nil {
		return err
	}
	// Search for the comment marker in the comments list.
	// If found, update the comment with the new suggestion.
	// If not found, create a new suggestion comment.
	found := slices.IndexFunc(comments, func(c pullRequestComment) bool {
		return c.Path == filePath && strings.Contains(c.Body, marker)
	})
	if found != -1 {
		if err := g.updateReviewComment(comments[found].ID, body); err != nil {
			return err
		}
		return nil
	}
	prComment := pullRequestComment{
		Body:     body,
		Path:     filePath,
		CommitID: g.event.PullRequest.Head.SHA,
	}
	if suggestion.TextEdit.End <= suggestion.TextEdit.Line {
		prComment.Line = suggestion.TextEdit.Line
	} else {
		prComment.StartLine = suggestion.TextEdit.Line
		prComment.Line = suggestion.TextEdit.End
	}
	buf, err := json.Marshal(prComment)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", g.baseURL, g.repo, g.event.PullRequest.Number)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
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
func (g *githubAPI) listReviewComments() ([]pullRequestComment, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", g.baseURL, g.repo, g.event.PullRequest.Number)
	req, err := http.NewRequest(http.MethodGet, url, nil)
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
func (g *githubAPI) updateReviewComment(id int, body string) error {
	type pullRequestUpdate struct {
		Body string `json:"body"`
	}
	b, err := json.Marshal(pullRequestUpdate{Body: body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/comments/%v", g.baseURL, g.repo, id)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(b))
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

// listPullRequestFiles return paths of the files in the trigger event pull request.
func (g *githubAPI) listPullRequestFiles() ([]string, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/files", g.baseURL, g.repo, g.event.PullRequest.Number)
	req, err := http.NewRequest(http.MethodGet, url, nil)
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

func createRunContext(act *githubactions.Action) (*atlasexec.RunContext, error) {
	ghContext, err := act.Context()
	if err != nil {
		return nil, fmt.Errorf("failed to load action context: %w", err)
	}
	ev, err := triggerEvent(ghContext)
	if err != nil {
		return nil, err
	}
	branch := ghContext.HeadRef
	if branch == "" {
		branch = ghContext.RefName
	}
	return &atlasexec.RunContext{
		Repo:   ghContext.Repository,
		Branch: branch,
		Commit: ghContext.SHA,
		Path:   act.GetInput("dir"),
		URL:    ev.HeadCommit.URL,
	}, nil
}

type githubTriggerEvent struct {
	HeadCommit struct {
		URL string `mapstructure:"url"`
	} `mapstructure:"head_commit"`
	PullRequest struct {
		Number int `mapstructure:"number"`
		Head   struct {
			SHA string `mapstructure:"sha"`
		} `mapstructure:"head"`
	} `mapstructure:"pull_request"`
}

// triggerEvent extracts the trigger event data from the action context.
func triggerEvent(ghContext *githubactions.GitHubContext) (*githubTriggerEvent, error) {
	var event githubTriggerEvent
	if err := mapstructure.Decode(ghContext.Event, &event); err != nil {
		return nil, fmt.Errorf("failed to parse push event: %v", err)
	}
	return &event, nil
}

// commentMarker creates a hidden marker to identify the comment as one created by this action.
func commentMarker(id string) string {
	return fmt.Sprintf(`<!-- generated by ariga/atlas-action for %v -->`, id)
}
