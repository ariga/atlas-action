// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"fmt"
	"io"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/mitchellh/mapstructure"
	"github.com/sethvargo/go-githubactions"
)

var _ Action = (*ghAction)(nil)

// ghAction is an implementation of the Action interface for GitHub Actions.
type ghAction struct {
	*githubactions.Action
}

// New returns a new Action for GitHub Actions.
func NewGHAction(getenv func(string) string, w io.Writer) *ghAction {
	return &ghAction{
		githubactions.New(
			githubactions.WithGetenv(getenv),
			githubactions.WithWriter(w),
		),
	}
}

// GetType implements the Action interface.
func (a *ghAction) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeGithubAction
}

// Context returns the context of the action.
func (a *ghAction) GetTriggerContext() (*TriggerContext, error) {
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
		}
	}
	return tc, nil
}

// WithFieldsMap return a new Logger with the given fields.
func (a *ghAction) WithFieldsMap(m map[string]string) Logger {
	return &ghAction{a.Action.WithFieldsMap(m)}
}

// githubTriggerEvent is the structure of the GitHub trigger event.
type githubTriggerEvent struct {
	PullRequest struct {
		Number int    `mapstructure:"number"`
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
