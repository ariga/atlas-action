// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
)

// gitlabCI is an implementation of the Action interface for Gitlab CI.
type gitlabCI struct {
	*colorsLogger
	getenv func(string) string
}

var _ Action = (*gitlabCI)(nil)

// NewGitlabCI returns a new Action for Gitlab CI.
func NewGitlabCI(getenv func(string) string, w io.Writer) Action {
	return &gitlabCI{getenv: getenv, colorsLogger: &colorsLogger{w}}
}

// GetType implements the Action interface.
func (g *gitlabCI) GetType() atlasexec.TriggerType {
	return "GITLAB_CI"
}

// GetInput implements the Action interface.
func (g *gitlabCI) GetInput(name string) string {
	e := strings.ReplaceAll(name, " ", "_")
	e = strings.ReplaceAll(e, "-", "_")
	e = strings.ToUpper(e)
	return strings.TrimSpace(g.getenv("ATLAS_INPUT_" + e))
}

// SetOutput implements the Action interface.
func (g *gitlabCI) SetOutput(name, value string) {
	f, err := os.OpenFile(".env", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s=%s\n", name, value)
}

// GetTriggerContext implements the Action interface.
func (g *gitlabCI) GetTriggerContext() (*TriggerContext, error) {
	ctx := &TriggerContext{
		SCM: SCM{
			Type:   atlasexec.SCMTypeGitlab,
			APIURL: g.getenv("CI_API_V4_URL"),
		},
		Repo:    g.getenv("CI_PROJECT_NAME"),
		RepoURL: g.getenv("CI_PROJECT_URL"),
		Branch:  g.getenv("CI_COMMIT_REF_NAME"),
		Commit:  g.getenv("CI_COMMIT_SHA"),
		Actor:   &Actor{Name: g.getenv("GITLAB_USER_NAME"), ID: g.getenv("GITLAB_USER_ID")},
	}
	if mr := g.getenv("CI_MERGE_REQUEST_IID"); mr != "" {
		num, err := strconv.Atoi(mr)
		if err != nil {
			return nil, err
		}
		ctx.PullRequest = &PullRequest{
			Commit: g.getenv("CI_COMMIT_SHA"),
			Number: num,
			URL:    g.getenv("CI_MERGE_REQUEST_REF_PATH"),
			Body:   g.getenv("CI_MERGE_REQUEST_DESCRIPTION"),
		}
	}
	return ctx, nil
}

// AddStepSummary implements the Action interface.
func (g *gitlabCI) AddStepSummary(summary string) {
	// unsupported
}

type gitlabTransport struct {
	Token string
}

func (t *gitlabTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("PRIVATE-TOKEN", t.Token)
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultTransport.RoundTrip(req)
}

func (g *gitlabCI) SCM() (SCMClient, error) {
	tc, err := g.GetTriggerContext()
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Timeout: time.Second * 30}
	if token := g.getenv("GITLAB_TOKEN"); token != "" {
		httpClient.Transport = &gitlabTransport{
			Token: token,
		}
	} else {
		g.Warningf("GITLAB_TOKEN is not set, the action may not have all the permissions")
	}
	return &gitlabAPI{
		baseURL: tc.SCM.APIURL,
		project: g.getenv("CI_PROJECT_ID"),
		client:  httpClient,
	}, nil
}

type gitlabAPI struct {
	baseURL string
	project string
	client  *http.Client
}

type mergeRequestFile struct {
	NewPath string `json:"new_path"`
}

type GitlabComment struct {
	ID     int    `json:"id"`
	Body   string `json:"body"`
	System bool   `json:"system"`
}

var _ SCMClient = (*gitlabAPI)(nil)

func (g *gitlabAPI) ListPullRequestFiles(ctx context.Context, pr *PullRequest) ([]string, error) {
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/diffs", g.baseURL, g.project, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return nil, fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var files []mergeRequestFile
	if err = json.NewDecoder(res.Body).Decode(&files); err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].NewPath
	}
	return paths, nil
}

func (g *gitlabAPI) UpsertSuggestion(context.Context, *PullRequest, *Suggestion) error {
	return errors.New("not supported")
}

func (g *gitlabAPI) UpsertComment(ctx context.Context, pr *PullRequest, id, comment string) error {
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/notes", g.baseURL, g.project, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("error querying gitlab comments with %v/%v, %w", g.project, pr.Number, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("error reading PR issue comments from %v/%v, %v", g.project, pr.Number, err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %v when calling Gitlab API. body: %s", res.StatusCode, string(buf))
	}
	var comments []GitlabComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return fmt.Errorf("error parsing gitlab notes with %v/%v from %v, %w", g.project, pr.Number, string(buf), err)
	}
	var (
		marker = commentMarker(id)
		body   = fmt.Sprintf(`{"body": %q}`, comment+"\n"+marker)
	)
	if found := slices.IndexFunc(comments, func(c GitlabComment) bool {
		return !c.System && strings.Contains(c.Body, marker)
	}); found != -1 {
		return g.updateComment(ctx, pr, comments[found].ID, body)
	}
	return g.createComment(ctx, pr, comment)
}

func (g *gitlabAPI) createComment(ctx context.Context, pr *PullRequest, comment string) error {
	body := strings.NewReader(fmt.Sprintf(`{"body": %q}`, comment))
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/notes", g.baseURL, g.project, pr.Number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
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

func (g *gitlabAPI) updateComment(ctx context.Context, pr *PullRequest, NoteId int, comment string) error {
	body := strings.NewReader(fmt.Sprintf(`{"body": %q}`, comment))
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/notes/%v", g.baseURL, g.project, pr.Number, NoteId)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	res, err := g.client.Do(req)
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
