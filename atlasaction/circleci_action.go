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
	ctx.RepoURL = os.Getenv("CIRCLE_REPOSITORY_URL")
	ctx.Branch = os.Getenv("CIRCLE_BRANCH")
	if ctx.Commit = os.Getenv("CIRCLE_SHA1"); ctx.Commit == "" {
		return nil, fmt.Errorf("missing CIRCLE_SHA1 environment variable")
	}
	// Detect SCM provider based on Token.
	switch {
	case os.Getenv("GITHUB_TOKEN") != "":
		ctx.SCM.Provider = ProviderGithub
		ctx.SCM.APIURL = defaultGHApiUrl
		// Used to change the location that the linting results are posted to.
		// If GITHUB_REPOSITORY is not set, we default to the CIRCLE_PROJECT_REPONAME repo.
		ghRepo := os.Getenv("GITHUB_REPOSITORY")
		if ghRepo != "" {
			ctx.Repo = ghRepo
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
		var err error
		ctx.PullRequest, err = getGHPR(ctx.Repo, ctx.Branch)
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

// getGHPR gets the newest open pull request for the branch.
func getGHPR(repo, branch string) (*PullRequest, error) {
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
	// Extract owner and repo from the GITHUB_REPOSITORY.
	s := strings.Split(repo, "/")
	if len(s) != 2 {
		return nil, fmt.Errorf("GITHUB_REPOSITORY must be in the format of 'owner/repo'")
	}
	// Get open pull requests for the branch.
	head := fmt.Sprintf("%s:%s", s[0], branch)
	req, err := client.Get(
		fmt.Sprintf("%s/repos/%s/pulls?state=open&head=%s&sort=created&direction=desc&per_page=1&page=1",
			defaultGHApiUrl,
			repo,
			head))
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
