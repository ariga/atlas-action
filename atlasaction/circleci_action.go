package atlasaction

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var _ Action = (*circleCIOrb)(nil)

// circleciOrb is an implementation of the Action interface for GitHub Actions.
type circleCIOrb struct {
	w io.Writer
}

// New returns a new Action for GitHub Actions.
func NewCircleCIOrb() Action {
	return &circleCIOrb{
		w: os.Stdout,
	}
}

// GetInput implements the Action interface.
func (a *circleCIOrb) GetInput(name string) string {
	e := strings.ReplaceAll(name, " ", "_")
	e = strings.ToUpper(e)
	e = "INPUT_" + e
	return strings.TrimSpace(os.Getenv(e))
}

// SetOutput implements the Action interface.
func (a *circleCIOrb) SetOutput(name, value string) {
	// unsupported
}

// GetTriggerContext implements the Action interface.
// https://circleci.com/docs/variables/#built-in-environment-variables
func (a *circleCIOrb) GetTriggerContext() (*TriggerContext, error) {
	ctx := &TriggerContext{}
	ctx.Repo = a.GetInput("CIRCLE_PR_REPONAME")
	ctx.RepoURL = a.GetInput("CIRCLE_REPOSITORY_URL")
	ctx.Branch = a.GetInput("CIRCLE_BRANCH")
	ctx.Commit = a.GetInput("CIRCLE_SHA1")
	// fill up PR information if the pr number is available.
	prNumber := a.GetInput("CIRCLE_PR_NUMBER")
	if prNumber != "" {
		n, err := strconv.Atoi(prNumber)
		if err != nil {
			return nil, fmt.Errorf("failed to parse PR number: %w", err)
		}
		ctx.PullRequest = &PullRequest{
			Number: n,
			URL:    a.GetInput("CIRCLE_PULL_REQUEST"),
			Commit: a.GetInput("CIRCLE_SHA1"),
		}
	}
	// Detect SCM provider based on the repository URL.
	switch {
	case strings.Contains(ctx.RepoURL, "github.com"):
		ctx.SCM.Provider = ProviderGithub
		ctx.SCM.APIURL = "https://api.github.com"
	default:
		return nil, fmt.Errorf("unsupported SCM provider")
	}
	return ctx, nil
}

// Line separator for logging.
const EOF = "\n"

// Infof implements the Logger interface.
func (o *circleCIOrb) Infof(msg string, args ...any) {
	fmt.Fprintf(o.w, "Info: "+msg+EOF, args...)
}

// Warningf implements the Logger interface.
func (o *circleCIOrb) Warningf(msg string, args ...any) {
	fmt.Fprintf(o.w, "Warning: "+msg+EOF, args...)
}

// Errorf implements the Logger interface.
func (o *circleCIOrb) Errorf(msg string, args ...any) {
	fmt.Fprintf(o.w, "Error: "+msg+EOF, args...)
}

// Fatalf implements the Logger interface.
func (a *circleCIOrb) Fatalf(msg string, args ...any) {
	a.Errorf(msg, args...)
	os.Exit(1)
}

// WithFieldsMap implements the Logger interface.
func (a *circleCIOrb) WithFieldsMap(map[string]string) Logger {
	// unsupported
	return a
}

// AddStepSummary implements the Action interface.
func (a *circleCIOrb) AddStepSummary(summary string) {
	// unsupported
}
