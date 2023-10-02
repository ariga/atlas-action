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
	"log"
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

// MigratePush runs the Github Action for "ariga/atlas-action/migrate/push"
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

// MigrateLint runs the Github Action for "ariga/atlas-action/migrate/lint"
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
	publishErr := publishLintComment(url, err, act)
	if publishErr != nil {
		fmt.Printf("unable to publish report: %v", publishErr)
	}
	return err
}

func publishLintComment(url string, err error, act *githubactions.Action) error {
	status := "success"
	if err != nil {
		status = "error"
	}
	icon := fmt.Sprintf(`<img src="https://release.ariga.io/images/assets/%v.svg"/>`, status)
	summary := fmt.Sprintf(`# Atlas Lint Report
<div>Analyzed <strong>%v</strong> %v </div><br>
<strong>Lint report <a href=%q>available here</a></strong>`, act.GetInput("dir-name"), icon, url)
	act.AddStepSummary(summary)

	g := NewGithub()
	ref := act.Getenv("GITHUB_REF")
	prNumber, err := strconv.Atoi(ref)
	if err != nil {
		return fmt.Errorf("unkown pr: %v, err: %w", ref, err)
	}
	repo := act.Getenv("GITHUB_REPO")
	if repo == "" {
		log.Printf("repository not found, can't publish comment")
		return errors.New("unknown github repository $GITHUB_REPO is empty")
	}
	ghToken := act.Getenv("GITHUB_TOKEN")
	comments, err := g.GetIssueComments(prNumber, repo, ghToken)
	if err != nil {
		return err
	}
	migrationDir := act.GetInput("dir-name")
	r, err := generateComment(summary, migrationDir)
	if err != nil {
		return err
	}
	if ac := findFirst(comments, isAtlasLintCommentFor(migrationDir)); ac != nil {
		err = g.UpdateComment(ac.Id, r, repo, ghToken)
	} else {
		err = g.CreateIssueComment(prNumber, r, repo, ghToken)
	}

	return err
}

type githubComment struct {
	Body string `json:"body"`
}

func generateComment(data, dir string) (io.Reader, error) {
	c := githubComment{
		fmt.Sprintf(data+"\n"+lintCommentTokenFormat, dir),
	}
	buf, err := json.Marshal(c)
	return bytes.NewReader(buf), err
}

func createContext(github *githubactions.Action) (*ContextInput, error) {
	ghContext, err := github.Context()
	if err != nil {
		return nil, fmt.Errorf("failed to load action context: %w", err)
	}
	var ev struct {
		HeadCommit struct {
			URL string `mapstructure:"url"`
		} `mapstructure:"head_commit"`
		Ref string `mapstructure:"ref"`
	}
	if err := mapstructure.Decode(ghContext.Event, &ev); err != nil {
		return nil, fmt.Errorf("failed to parse push event: %v", err)
	}
	return &ContextInput{
		Repo:   ghContext.Repository,
		Branch: ghContext.RefName,
		Commit: github.Getenv("GITHUB_SHA"),
		Path:   github.GetInput("dir"),
		URL:    ev.HeadCommit.URL,
	}, nil
}

func findFirst(comments []GithubIssueComment, filter func(comment *GithubIssueComment) bool) *GithubIssueComment {
	for i, _ := range comments {
		c := &comments[i]
		if filter(c) {
			return c
		}
	}
	return nil
}

func isAtlasLintCommentFor(dir string) func(c *GithubIssueComment) bool {
	return func(c *GithubIssueComment) bool {
		return strings.Contains(c.Body, fmt.Sprintf(lintCommentTokenFormat, dir))
	}
}

const lintCommentTokenFormat = `<!-- generated by ariga/atlas-action for %v -->`
