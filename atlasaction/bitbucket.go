// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package atlasaction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas-go-sdk/atlasexec"
	"github.com/fatih/color"
	"golang.org/x/oauth2"
)

type bbPipe struct {
	*coloredLogger
	getenv func(string) string
}

// NewBitBucketPipe returns a new Action for BitBucket.
func NewBitBucketPipe(getenv func(string) string, w io.Writer) Action {
	// Disable color output for testing,
	// but enable it for non-testing environments.
	color.NoColor = testing.Testing()
	return &bbPipe{getenv: getenv, coloredLogger: &coloredLogger{w: w}}
}

// GetType implements Action.
func (a *bbPipe) GetType() atlasexec.TriggerType {
	return atlasexec.TriggerTypeBitbucket
}

// GetTriggerContext implements Action.
func (a *bbPipe) GetTriggerContext() (*TriggerContext, error) {
	tc := &TriggerContext{
		Branch:  a.getenv("BITBUCKET_BRANCH"),
		Commit:  a.getenv("BITBUCKET_COMMIT"),
		Repo:    a.getenv("BITBUCKET_REPO_FULL_NAME"),
		RepoURL: a.getenv("BITBUCKET_GIT_HTTP_ORIGIN"),
		SCM: SCM{
			Type:   atlasexec.SCMTypeBitbucket,
			APIURL: "https://api.bitbucket.org/2.0",
		},
	}
	if pr := a.getenv("BITBUCKET_PR_ID"); pr != "" {
		var err error
		tc.PullRequest = &PullRequest{
			Commit: a.getenv("BITBUCKET_COMMIT"),
		}
		tc.PullRequest.Number, err = strconv.Atoi(pr)
		if err != nil {
			return nil, err
		}
		// <repo-url>/pull-requests/<pr-id>
		tc.PullRequest.URL, err = url.JoinPath(tc.RepoURL, "pull-requests", pr)
		if err != nil {
			return nil, err
		}
	}
	return tc, nil
}

// GetInput implements the Action interface.
func (a *bbPipe) GetInput(name string) string {
	return strings.TrimSpace(a.getenv("ATLAS_INPUT_" + toEnvVar(name)))
}

// SetOutput implements Action.
func (a *bbPipe) SetOutput(name, value string) {
	// Because Bitbucket Pipes does not support output variables,
	// we write the output to a file.
	// So the next step can read the outputs using the source command.
	// e.g:
	// ```shell
	// source $BITBUCKET_PIPE_STORAGE_DIR/outputs.sh
	// ```
	// https://support.atlassian.com/bitbucket-cloud/docs/advanced-techniques-for-writing-pipes/#Sharing-information-between-pipes
	dir := a.getenv("BITBUCKET_PIPE_STORAGE_DIR")
	if out := a.getenv("OUTPUT_DIR"); out != "" {
		// The user can set the output directory using
		// the OUTPUT_DIR environment variable.
		// This is useful when the user wants to share the output
		// with steps run outside the pipe.
		dir = out
	}
	if dir == "" {
		return
	}
	cmd := a.getenv("ATLAS_ACTION_COMMAND")
	err := writeBashEnv(filepath.Join(dir, "outputs.sh"), toEnvVar(
		fmt.Sprintf("ATLAS_OUTPUT_%s_%s", cmd, name)), value)
	if err != nil {
		a.Errorf("failed to write output to file %s: %v", dir, err)
	}
}

func (a *bbPipe) AddStepSummary(string) {}

type bbClient struct {
	baseURL   string
	workspace string
	repoSlug  string
	client    *http.Client
}

func BitbucketClient(workspace, repoSlug, baseURL, token string) *bbClient {
	httpClient := &http.Client{Timeout: time.Second * 30}
	if token != "" {
		httpClient.Transport = &oauth2.Transport{
			Base: http.DefaultTransport,
			Source: oauth2.StaticTokenSource(&oauth2.Token{
				AccessToken: token,
			}),
		}
	}
	return &bbClient{
		baseURL:   baseURL,
		workspace: workspace,
		repoSlug:  repoSlug,
		client:    httpClient,
	}
}

// UpsertComment implements SCMClient.
func (b *bbClient) UpsertComment(ctx context.Context, pr *PullRequest, id string, comment string) error {
	c, err := b.listComments(ctx, pr)
	if err != nil {
		return err
	}
	var (
		marker = commentMarker(id)
		body   = strings.NewReader(fmt.Sprintf(`{"content":{"raw":%q}}`, comment+"\n"+marker))
	)
	if found := slices.IndexFunc(c, func(c BitBucketComment) bool {
		return strings.Contains(c.Body, marker)
	}); found != -1 {
		return b.updateComment(ctx, pr, c[found].ID, body)
	}
	return b.createComment(ctx, pr, body)
}

type (
	BitBucketComment struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}
	BitBucketPaginatedResponse[T any] struct {
		Size     int    `json:"size"`
		Page     int    `json:"page"`
		PageLen  int    `json:"pagelen"`
		Next     string `json:"next"`
		Previous string `json:"previous"`
		Values   []T    `json:"values"`
	}
)

func (b *bbClient) listComments(ctx context.Context, pr *PullRequest) (result []BitBucketComment, err error) {
	u, err := b.commentsURL(pr)
	if err != nil {
		return nil, err
	}
	for u != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		res, err := b.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status code %d", res.StatusCode)
		}
		var comments BitBucketPaginatedResponse[struct {
			ID      int `json:"id"`
			Content struct {
				Raw string `json:"raw"`
			} `json:"content"`
		}]
		if err := json.NewDecoder(io.TeeReader(res.Body, os.Stdout)).Decode(&comments); err != nil {
			return nil, err
		}
		for _, c := range comments.Values {
			result = append(result, BitBucketComment{ID: c.ID, Body: c.Content.Raw})
		}
		u = comments.Next // Fetch the next page if available.
	}
	return result, nil
}

func (b *bbClient) createComment(ctx context.Context, pr *PullRequest, content io.Reader) error {
	u, err := b.commentsURL(pr)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, content)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code %d", res.StatusCode)
	}
	return nil
}

func (b *bbClient) updateComment(ctx context.Context, pr *PullRequest, commentID int, content io.Reader) error {
	u, err := b.commentsURL(pr)
	if err != nil {
		return err
	}
	u, err = url.JoinPath(u, strconv.Itoa(commentID))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, content)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d", res.StatusCode)
	}
	return nil
}

func (b *bbClient) commentsURL(pr *PullRequest) (string, error) {
	return url.JoinPath(b.baseURL, "repositories", b.workspace, b.repoSlug, "pullrequests", strconv.Itoa(pr.Number), "comments")
}

var _ Action = (*bbPipe)(nil)
var _ SCMClient = (*bbClient)(nil)
