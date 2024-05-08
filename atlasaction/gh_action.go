package atlasaction

import (
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

// Context returns the context of the action.
func (a *ghAction) GetTriggerContext() (*TriggerContext, error) {
	ctx, err := a.Action.Context()
	if err != nil {
		return nil, err
	}
	return &TriggerContext{
		Repository: ctx.Repository,
		Event:      ctx.Event,
		EventName:  ctx.EventName,
		HeadRef:    ctx.HeadRef,
		RefName:    ctx.RefName,
		APIURL:     ctx.APIURL,
		SHA:        ctx.SHA,
	}, nil
}

// WithFieldsMap return a new Logger with the given fields.
func (a *ghAction) WithFieldsMap(m map[string]string) Logger {
	return &ghAction{a.Action.WithFieldsMap(m)}
}
