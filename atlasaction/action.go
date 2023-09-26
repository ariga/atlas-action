// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

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
	if tag != "" {
		params.Tag = tag
	} else {
		params.Tag = ghContext.Commit
	}
	resp, err := client.MigratePush(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to push dir tag: %w", err)
	}
	act.SetOutput("url", resp)
	act.Infof("Uploaded migration dir %q to Atlas Cloud\n", params.Name)
	return nil
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
