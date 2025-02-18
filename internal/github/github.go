// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"
	"golang.org/x/oauth2"
)

type (
	// Client is an implementation of the SCMClient interface for GitHub Actions.
	Client struct {
		baseURL string
		repo    string
		client  *http.Client
	}
	// ClientOption is the option when creating a new client.
	ClientOption func(*Client) error
	IssueComment struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}
	PullRequestComment struct {
		ID        int    `json:"id,omitempty"`
		Body      string `json:"body"`
		Path      string `json:"path"`
		CommitID  string `json:"commit_id,omitempty"`
		StartLine int    `json:"start_line,omitempty"`
		Line      int    `json:"line,omitempty"`
	}
	PullRequestFile struct {
		Patch  string `json:"patch"`
		Status string `json:"status"`
		Name   string `json:"filename"`
	}
	PullRequest struct {
		Number int
		URL    string
		Body   string
		Commit string
	}
	// TriggerEvent is the structure of the GitHub trigger event.
	TriggerEvent struct {
		PullRequest struct {
			Number int    `mapstructure:"number"`
			Body   string `mapstructure:"body"`
			URL    string `mapstructure:"html_url"`
			Head   struct {
				SHA string `mapstructure:"sha"`
			} `mapstructure:"head"`
		} `mapstructure:"pull_request"`
		Repository struct {
			URL string `mapstructure:"html_url"`
		} `mapstructure:"repository"`
	}
)

const DefaultBaseURL = "https://api.github.com"

// WithBaseURL returns a ClientOption that sets the base URL for the client.
func WithBaseURL(url string) ClientOption {
	return func(c *Client) error {
		c.baseURL = url
		return nil
	}
}

// WithToken returns a ClientOption that sets the token for the client.
func WithToken(t *oauth2.Token) ClientOption {
	return func(c *Client) error {
		base := c.client.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		c.client.Transport = &oauth2.Transport{
			Base:   base,
			Source: oauth2.StaticTokenSource(t),
		}
		return nil
	}
}

// NewClient returns a new GitHub client for the given repository.
// If the GITHUB_TOKEN is set, it will be used for authentication.
func NewClient(repo string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		repo:    repo,
		baseURL: DefaultBaseURL,
		client:  &http.Client{Timeout: time.Second * 30},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	return c, nil
}

func (c *Client) GetURL(ctx context.Context, path string, dst any) error {
	url := fmt.Sprintf("%s/%s", c.baseURL, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d when calling GitHub API", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func (c *Client) IssueComments(ctx context.Context, prID int) ([]IssueComment, error) {
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", c.baseURL, c.repo, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying github comments with %v/%v, %w", c.repo, prID, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", c.repo, prID, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling GitHub API", res.StatusCode)
	}
	var comments []IssueComment
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing github comments with %v/%v from %v, %w", c.repo, prID, string(buf), err)
	}
	return comments, nil
}

func (c *Client) CreateIssueComment(ctx context.Context, prID int, comment string) error {
	content := strings.NewReader(fmt.Sprintf(`{"body":%q}`, comment))
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", c.baseURL, c.repo, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, content)
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
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

// UpdateIssueComment updates issue comment with the given id.
func (c *Client) UpdateIssueComment(ctx context.Context, id int, comment string) error {
	content := strings.NewReader(fmt.Sprintf(`{"body":%q}`, comment))
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", c.baseURL, c.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, content)
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
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

// ReviewComments for the trigger event pull request.
func (c *Client) ReviewComments(ctx context.Context, prID int) ([]PullRequestComment, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", c.baseURL, c.repo, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
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
	var comments []PullRequestComment
	if err = json.NewDecoder(res.Body).Decode(&comments); err != nil {
		return nil, err
	}
	return comments, nil
}

func (c *Client) CreateReviewComment(ctx context.Context, prID int, s *PullRequestComment) error {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/comments", c.baseURL, c.repo, prID)
	buf, err := json.Marshal(s)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
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

// UpdateReviewComment updates the review comment with the given id.
func (c *Client) UpdateReviewComment(ctx context.Context, id int, body string) error {
	type pullRequestUpdate struct {
		Body string `json:"body"`
	}
	b, err := json.Marshal(pullRequestUpdate{Body: body})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/pulls/comments/%v", c.baseURL, c.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
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

// ListPullRequestFiles return paths of the files in the trigger event pull request.
func (c *Client) ListPullRequestFiles(ctx context.Context, prID int) ([]string, error) {
	url := fmt.Sprintf("%v/repos/%v/pulls/%v/files", c.baseURL, c.repo, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
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
	var files []PullRequestFile
	if err = json.NewDecoder(res.Body).Decode(&files); err != nil {
		return nil, err
	}
	paths := make([]string, len(files))
	for i := range files {
		paths[i] = files[i].Name
	}
	return paths, nil
}

// OpeningPullRequest returns the latest open pull request for the given branch.
func (c *Client) OpeningPullRequest(ctx context.Context, branch string) (*PullRequest, error) {
	owner, _, err := c.ownerRepo()
	if err != nil {
		return nil, err
	}
	// Get open pull requests for the branch.
	url := fmt.Sprintf("%s/repos/%s/pulls?state=open&head=%s:%s&sort=created&direction=desc&per_page=1&page=1",
		c.baseURL, c.repo, owner, branch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	switch buf, err := io.ReadAll(res.Body); {
	case err != nil:
		return nil, fmt.Errorf("error reading open pull requests: %w", err)
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("unexpected status code: %d when calling GitHub API", res.StatusCode)
	default:
		var resp []struct {
			URL    string `json:"url"`
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
			URL:    resp[0].URL,
			Commit: resp[0].Head.Sha,
		}, nil
	}
}

func (c *Client) ownerRepo() (string, string, error) {
	s := strings.Split(c.repo, "/")
	if len(s) != 2 {
		return "", "", fmt.Errorf("GITHUB_REPOSITORY must be in the format of 'owner/repo'")
	}
	return s[0], s[1], nil
}

// ExtractEvent extracts the trigger event data from the raw event.
func ExtractEvent(raw map[string]any) (*TriggerEvent, error) {
	var event TriggerEvent
	if err := mapstructure.Decode(raw, &event); err != nil {
		return nil, fmt.Errorf("failed to parse push event: %v", err)
	}
	return &event, nil
}
