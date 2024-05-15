package atlasaction

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
)

var (
	defaultGHApiUrl        = "https://api.github.com"
	_               Action = (*circleCIOrb)(nil)
)

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
	if ctx.RepoURL = os.Getenv("CIRCLE_REPOSITORY_URL"); ctx.RepoURL == "" {
		return nil, fmt.Errorf("missing CIRCLE_REPOSITORY_URL environment variable")
	}
	if ctx.Branch = os.Getenv("CIRCLE_BRANCH"); ctx.Branch == "" {
		return nil, fmt.Errorf("missing CIRCLE_BRANCH environment variable")
	}
	if ctx.Commit = os.Getenv("CIRCLE_SHA1"); ctx.Commit == "" {
		return nil, fmt.Errorf("missing CIRCLE_SHA1 environment variable")
	}
	username := os.Getenv("CIRCLE_PROJECT_USERNAME")
	if username == "" {
		return nil, fmt.Errorf("missing CIRCLE_PROJECT_USERNAME environment variable")
	}
	// Detect SCM provider based on the repository URL.
	switch {
	case strings.Contains(ctx.RepoURL, "github.com"):
		ctx.SCM.Provider = ProviderGithub
		ctx.SCM.APIURL = defaultGHApiUrl
		// get open pull requests for the branch.
		var err error
		ctx.PullRequest, err = getGHPR(username, ctx.Repo, ctx.Branch)
		if err != nil {
			return nil, fmt.Errorf("failed to get open pull requests: %w", err)
		}
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

// getGHPR gets the newest open pull request for the branch.
func getGHPR(username, repo, branch string) (*PullRequest, error) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("missing GITHUB_TOKEN environment variable")
	}
	client := &http.Client{
		Transport: &roundTripper{
			authToken: token,
		},
		Timeout: time.Second * 30,
	}
	// get open pull requests for the branch.
	req, err := client.Get(
		fmt.Sprintf("%s/repos/%s/%s/pulls?state=open&head=%s&sort=created&direction=desc&per_page=1&page=1",
			defaultGHApiUrl,
			username,
			repo,
			branch))
	if err != nil {
		return nil, err
	}
	defer req.Body.Close()
	buf, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading open pull requests: %w", err)
	}
	if req.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d when calling GitHub API", req.StatusCode)
	}
	var resp []struct {
		Url    string `json:"url"`
		Number int    `json:"number"`
		Head   struct {
			Sha string `json:"sha"`
		} `json:"head"`
	}
	if err = json.Unmarshal(buf, &resp); err != nil {
		return nil, err
	}
	if len(resp) == 0 {
		return nil, nil
	}
	return &PullRequest{
		Number: resp[0].Number,
		URL:    resp[0].Url,
		Commit: resp[0].Head.Sha,
	}, nil
}
