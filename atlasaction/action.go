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
	"slices"
	"strconv"
	"strings"
	"text/template"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/mitchellh/mapstructure"
	"github.com/sethvargo/go-githubactions"
)

// version holds atlas-action version. When built with cloud packages should be set by build flag, e.g.
// "-X 'ariga.io/atlas-action/atlasaction.Version=v0.1.2'"
var Version string

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func MigrateApply(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	params := &atlasexec.MigrateApplyParams{
		URL:             act.GetInput("url"),
		DirURL:          act.GetInput("dir"),
		ConfigURL:       act.GetInput("config"),
		Env:             act.GetInput("env"),
		TxMode:          act.GetInput("tx-mode"),  // Hidden param.
		BaselineVersion: act.GetInput("baseline"), // Hidden param.
		Context: &atlasexec.DeployRunContext{
			TriggerType:    "GITHUB_ACTION",
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
	if err != nil && !errors.Is(err, atlasexec.LintErr) {
		return err
	}
	if err := json.NewDecoder(&resp).Decode(&payload); err != nil {
		return fmt.Errorf("decoding payload: %w", err)
	}
	if payload.URL != "" {
		act.SetOutput("report-url", payload.URL)
	}
	dirName := act.GetInput("dir-name")
	var summary bytes.Buffer
	if err := comment.Execute(&summary, &payload); err != nil {
		return err
	}
	if err := publish(act, dirName, summary.String()); err != nil {
		return err
	}
	if errors.Is(err, atlasexec.LintErr) {
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

// publish writes a comment and summary to the pull request for dirName. It adds a marker
// HTML comment to the end of the comment body to identify the comment as one created by
// this action.
func publish(act *githubactions.Action, dirName, summary string) error {
	ghContext, err := act.Context()
	if err != nil {
		return err
	}
	event, err := triggerEvent(ghContext)
	if err != nil {
		return err
	}
	act.AddStepSummary(summary)
	g := githubAPI{
		baseURL: ghContext.APIURL,
	}
	prNumber := event.PullRequestNumber
	if prNumber == 0 {
		return nil
	}
	ghToken := act.Getenv("GITHUB_TOKEN")
	comments, err := g.getIssueComments(prNumber, ghContext.Repository, ghToken)
	if err != nil {
		return err
	}
	marker := commentMarker(dirName)
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
		return g.updateComment(comments[found].ID, r, ghContext.Repository, ghToken)
	}
	return g.createIssueComment(prNumber, r, ghContext.Repository, ghToken)
}

type (
	githubIssueComment struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}

	githubAPI struct {
		baseURL string
	}
)

func (g *githubAPI) getIssueComments(id int, repo, authToken string) ([]githubIssueComment, error) {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, repo, id)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	addHeaders(req, authToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", repo, id, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", repo, id, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling GitHub API", res.StatusCode)
	}
	var comments []githubIssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", repo, id, string(buf), err)
	}
	return comments, nil
}

func (g *githubAPI) createIssueComment(id int, content io.Reader, repo, authToken string) error {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", g.baseURL, repo, id)
	req, err := http.NewRequest(http.MethodPost, url, content)
	if err != nil {
		return err
	}
	addHeaders(req, authToken)
	res, err := http.DefaultClient.Do(req)
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

func (g *githubAPI) updateComment(id int, content io.Reader, repo, authToken string) error {
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", g.baseURL, repo, id)
	req, err := http.NewRequest(http.MethodPatch, url, content)
	if err != nil {
		return err
	}
	addHeaders(req, authToken)
	res, err := http.DefaultClient.Do(req)
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

func addHeaders(req *http.Request, authToken string) {
	req.Header.Add("Accept", "application/vnd.github+json")
	req.Header.Add("Authorization", "Bearer "+authToken)
	req.Header.Add("X-GitHub-Api-Version", "2022-11-28")
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
	PullRequestNumber int `mapstructure:"number"`
}

func triggerEvent(ghContext *githubactions.GitHubContext) (*githubTriggerEvent, error) {
	var event githubTriggerEvent
	if err := mapstructure.Decode(ghContext.Event, &event); err != nil {
		return nil, fmt.Errorf("failed to parse push event: %v", err)
	}
	return &event, nil
}

func commentMarker(dirName string) string {
	return fmt.Sprintf(`<!-- generated by ariga/atlas-action for %v -->`, dirName)
}
