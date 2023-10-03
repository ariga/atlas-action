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
	"golang.org/x/exp/slices"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/mitchellh/mapstructure"
	"github.com/sethvargo/go-githubactions"
)

type (
	// ContextInput is passed to atlas as a json string to add additional information
	ContextInput struct {
		Repo   string `json:"repo"`
		Path   string `json:"path"`
		Branch string `json:"branch"`
		Commit string `json:"commit"`
		URL    string `json:"url"`
	}
)

// MigrateApply runs the GitHub Action for "ariga/atlas-action/migrate/apply".
func MigrateApply(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	params := &atlasexec.MigrateApplyParams{
		URL:             act.GetInput("url"),
		DirURL:          act.GetInput("dir"),
		ConfigURL:       act.GetInput("config"),
		Env:             act.GetInput("env"),
		TxMode:          act.GetInput("tx-mode"),  // Hidden param.
		BaselineVersion: act.GetInput("baseline"), // Hidden param.
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
	act.Infof("Run complete: +%v", run)
	return nil
}

// MigratePush runs the GitHub Action for "ariga/atlas-action/migrate/push"
func MigratePush(ctx context.Context, client *atlasexec.Client, act *githubactions.Action) error {
	ghContext, err := createContext(act)
	if err != nil {
		return fmt.Errorf("failed to read github metadata: %w", err)
	}
	buf, err := json.Marshal(ghContext)
	if err != nil {
		return fmt.Errorf("failed to create MigratePushParams: %w", err)
	}
	params := &atlasexec.MigratePushParams{
		Name:      act.GetInput("dir-name"),
		DirURL:    act.GetInput("dir"),
		DevURL:    act.GetInput("dev-url"),
		Context:   string(buf),
		ConfigURL: act.GetInput("config"),
		Env:       act.GetInput("env"),
	}
	_, err = client.MigratePush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push directory: %v", err)
	}
	tag := act.GetInput("tag")
	params.Tag = ghContext.Commit
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
	ghContext, err := createContext(act)
	if err != nil {
		return fmt.Errorf("failed to read github metadata: %w", err)
	}
	buf, err := json.Marshal(ghContext)
	if err != nil {
		return err
	}
	var resp bytes.Buffer
	err = client.MigrateLintError(ctx, &atlasexec.MigrateLintParams{
		DevURL:    act.GetInput("dev-url"),
		DirURL:    act.GetInput("dir"),
		ConfigURL: act.GetInput("config"),
		Env:       act.GetInput("env"),
		Base:      "atlas://" + act.GetInput("dir-name"),
		Context:   string(buf),
		Web:       true,
		Writer:    &resp,
	})
	url := strings.TrimSpace(resp.String())
	act.SetOutput("report-url", url)
	if publishErr := publishResult(url, err, act); publishErr != nil {
		act.Warningf("unable to publish lint report: %v", publishErr)
	}
	return err
}

func publishResult(url string, err error, act *githubactions.Action) error {
	status := "success"
	if err != nil {
		status = "error"
	}
	ghContext, err := act.Context()
	if err != nil {
		return err
	}
	event, err := triggerEvent(ghContext)
	if err != nil {
		return err
	}
	icon := fmt.Sprintf(`<img src="https://release.ariga.io/images/assets/%v.svg"/>`, status)
	dirName := act.GetInput("dir-name")
	summary := fmt.Sprintf(`# Atlas Lint Report
<div>Analyzed <strong>%v</strong> %v </div><br>
<strong>Lint report <a href=%q>available here</a></strong>`, dirName, icon, url)
	act.AddStepSummary(summary)
	g := githubAPI{
		baseURL: ghContext.APIURL,
	}
	prNumber := event.PullRequestNumber
	if prNumber == 0 {
		return nil
	}
	ghToken := act.GetInput("github-token")
	comments, err := g.getIssueComments(prNumber, event.Repository.Name, ghToken)
	if err != nil {
		return err
	}
	comment := struct {
		Body string `json:"body"`
	}{
		Body: fmt.Sprintf(summary+"\n"+lintCommentTokenFormat, dirName),
	}
	buf, err := json.Marshal(comment)
	if err != nil {
		return err
	}
	r := bytes.NewReader(buf)
	found := slices.IndexFunc(comments, func(c githubIssueComment) bool {
		return strings.Contains(c.Body, fmt.Sprintf(lintCommentTokenFormat, dirName))
	})
	if found != -1 {
		err = g.updateComment(comments[found].ID, r, event.Repository.Name, ghToken)
	} else {
		err = g.createIssueComment(prNumber, r, event.Repository.Name, ghToken)
	}
	return err
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
	url := fmt.Sprintf("%v/repos/ariga/%v/issues/%v/comments", g.baseURL, repo, id)
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
		return nil, err
	}
	var comments []githubIssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", repo, id, string(buf), err)
	}
	return comments, nil
}

func (g *githubAPI) createIssueComment(id int, content io.Reader, repo, authToken string) error {
	url := fmt.Sprintf("%v/repos/ariga/%v/issues/%v/comments", g.baseURL, repo, id)
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
	url := fmt.Sprintf("%v/repos/ariga/%v/issues/comments/%v", g.baseURL, repo, id)
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

func createContext(act *githubactions.Action) (*ContextInput, error) {
	ghContext, err := act.Context()
	if err != nil {
		return nil, fmt.Errorf("failed to load action context: %w", err)
	}
	ev, err := triggerEvent(ghContext)
	if err != nil {
		return nil, err
	}
	return &ContextInput{
		Repo:   ghContext.Repository,
		Branch: ghContext.RefName,
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
	Repository        struct {
		Name string `mapstructure:"name"`
	} `mapstructure:"repository"`
}

func triggerEvent(ghContext *githubactions.GitHubContext) (*githubTriggerEvent, error) {
	var event githubTriggerEvent
	if err := mapstructure.Decode(ghContext.Event, &event); err != nil {
		return nil, fmt.Errorf("failed to parse push event: %v", err)
	}
	return &event, nil
}

const lintCommentTokenFormat = `<!-- generated by ariga/atlas-action for %v -->`
