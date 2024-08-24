package atlasaction

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
)

// circleciOrb is an implementation of the Action interface for GitHub Actions.
type circleCIOrb struct {
	w io.Writer
}

var _ Action = (*circleCIOrb)(nil)

// New returns a new Action for GitHub Actions.
func NewCircleCIOrb() Action {
	return &circleCIOrb{
		w: os.Stdout,
	}
}

// GetType implements the Action interface.
func (a *circleCIOrb) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeCircleCIOrb
}

// GetInput implements the Action interface.
func (a *circleCIOrb) GetInput(name string) string {
	e := strings.ReplaceAll(name, " ", "_")
	e = strings.ReplaceAll(e, "-", "_")
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
	if ctx.Repo = os.Getenv("CIRCLE_PROJECT_REPONAME"); ctx.Repo == "" {
		return nil, fmt.Errorf("missing CIRCLE_PROJECT_REPONAME environment variable")
	}
	ctx.RepoURL = os.Getenv("CIRCLE_REPOSITORY_URL")
	ctx.Branch = os.Getenv("CIRCLE_BRANCH")
	if ctx.Commit = os.Getenv("CIRCLE_SHA1"); ctx.Commit == "" {
		return nil, fmt.Errorf("missing CIRCLE_SHA1 environment variable")
	}
	// Detect SCM provider based on Token.
	switch ghToken := os.Getenv("GITHUB_TOKEN"); {
	case ghToken != "":
		ctx.SCM = SCM{
			Provider: ProviderGithub,
			APIURL:   defaultGHApiUrl,
		}
		if v := os.Getenv("GITHUB_API_URL"); v != "" {
			ctx.SCM.APIURL = v
		}
		// Used to change the location that the linting results are posted to.
		// If GITHUB_REPOSITORY is not set, we default to the CIRCLE_PROJECT_REPONAME repo.
		if v := os.Getenv("GITHUB_REPOSITORY"); v != "" {
			ctx.Repo = v
		}
		// CIRCLE_REPOSITORY_URL will be empty for some reason, causing ctx.RepoURL to be empty.
		// In this case, we default to the GitHub Cloud URL.
		if ctx.RepoURL == "" {
			ctx.RepoURL = fmt.Sprintf("https://github.com/%s", ctx.Repo)
		}
		// CIRCLE_BRANCH will be empty when the event is triggered by a tag.
		// In this case, we can use CIRCLE_TAG as the branch.
		if ctx.Branch == "" {
			tag := os.Getenv("CIRCLE_TAG")
			if tag == "" {
				return nil, fmt.Errorf("cannot determine branch due to missing CIRCLE_BRANCH and CIRCLE_TAG environment variables")
			}
			ctx.Branch = tag
			return ctx, nil
		}
		// get open pull requests for the branch.
		c := &githubAPI{
			client: &http.Client{
				Timeout:   time.Second * 30,
				Transport: &roundTripper{authToken: ghToken},
			},
			baseURL: ctx.SCM.APIURL,
			repo:    ctx.Repo,
		}
		var err error
		ctx.PullRequest, err = c.OpeningPullRequest(context.Background(), ctx.Branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get open pull requests: %w", err)
		}
	}
	return ctx, nil
}

// Line separator for logging.
const EOF = "\n"

// Infof implements the Logger interface.
func (o *circleCIOrb) Infof(msg string, args ...any) {
	fmt.Fprintf(o.w, msg+EOF, args...)
}

// Warningf implements the Logger interface.
func (o *circleCIOrb) Warningf(msg string, args ...any) {
	fmt.Fprintf(o.w, msg+EOF, args...)
}

// Errorf implements the Logger interface.
func (o *circleCIOrb) Errorf(msg string, args ...any) {
	fmt.Fprintf(o.w, msg+EOF, args...)
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
