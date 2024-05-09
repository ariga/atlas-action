package atlasaction

import (
	"fmt"

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
func NewGHAction(otps ...githubactions.Option) Action {
	return &ghAction{githubactions.New(otps...)}
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
	// HeadRef will be empty for push events, so we use RefName instead.
	branch := ctx.HeadRef
	if branch == "" {
		branch = ctx.RefName
	}
	// SCM information.
	scm := SCM{
		Provider: ProviderGithub,
		APIURL:   ctx.APIURL,
	}
	// Extract the event data to fill up Repo and PR information.
	ghEvent, err := extractEvent(ctx.Event)
	if err != nil {
		return nil, err
	}
	var repoURL string
	if ghEvent.Repository.URL != "" {
		repoURL = ghEvent.Repository.URL
	}
	var pr *PullRequest
	if ctx.EventName == "pull_request" {
		pr = &PullRequest{
			Number: ghEvent.PullRequest.Number,
			URL:    ghEvent.PullRequest.URL,
			Commit: ghEvent.PullRequest.Head.SHA,
		}
	}
	return &TriggerContext{
		SCM:         scm,
		Repo:        ctx.Repository,
		RepoURL:     repoURL,
		Branch:      branch,
		Commit:      ctx.SHA,
		PullRequest: pr,
	}, nil
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
