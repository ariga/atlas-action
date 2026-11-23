// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
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
	pullRequestFile struct {
		Name string `json:"filename"`
	}
	PullRequest struct {
		Number int
		URL    string
		Body   string
		Commit string
		Ref    string
		Base   string // Base branch of the pull request.
		Stack  int    // Number of the stack the pull request belongs to, or 0 if it is not stacked.
	}
	// Stack is a stack of pull requests: a chain of pull requests in the same repository,
	// where the bottom one targets the stack base (its trunk) and each of the others
	// targets the head branch of the pull request below it.
	Stack struct {
		Number int  `json:"number"`
		Open   bool `json:"open"`
		Base   struct {
			Ref string `json:"ref"`
		} `json:"base"`
		// PullRequests of the stack, from the bottom to the top.
		PullRequests []StackPullRequest `json:"pull_requests"`
	}
	// StackPullRequest is a pull request in a stack.
	StackPullRequest struct {
		Number   int     `json:"number"`
		State    string  `json:"state"` // Either "open" or "closed".
		Draft    bool    `json:"draft"`
		MergedAt *string `json:"merged_at"`
		Head     struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		// Base of the pull request. SHA is the base branch commit GitHub last
		// compared the pull request with, so it may lag behind the branch.
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	}
	// TriggerEvent is the structure of the GitHub trigger event.
	TriggerEvent struct {
		// Only in case of a 'push' event. The commit the ref pointed to before the push.
		Before string `mapstructure:"before"`
		// Only in case of an 'issue_comment' event.
		Comment struct {
			Body string `mapstructure:"body"`
			URL  string `mapstructure:"html_url"`
		} `mapstructure:"comment"`
		Issue struct {
			Number      int       `mapstructure:"number"`
			URL         string    `mapstructure:"html_url"`
			PullRequest *struct{} `mapstructure:"pull_request"` // not-nil if the issue is a pull request
		} `mapstructure:"issue"`

		// Only in case of a 'pull_request' event.
		PullRequest struct {
			Number int    `mapstructure:"number"`
			Body   string `mapstructure:"body"`
			URL    string `mapstructure:"html_url"`
			Head   struct {
				SHA string `mapstructure:"sha"`
			} `mapstructure:"head"`
		} `mapstructure:"pull_request"`
		Repository struct {
			URL           string `mapstructure:"html_url"`
			DefaultBranch string `mapstructure:"default_branch"`
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
	b, err := json.Marshal(map[string]string{"body": comment})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/issues/%v/comments", c.baseURL, c.repo, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
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
	b, err := json.Marshal(map[string]string{"body": comment})
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", c.baseURL, c.repo, id)
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

// DeleteIssueComment deletes the issue comment with the given id.
func (c *Client) DeleteIssueComment(ctx context.Context, id int) error {
	url := fmt.Sprintf("%v/repos/%v/issues/comments/%v", c.baseURL, c.repo, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		b, err := io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("unexpected status code %v: unable to read body %v", res.StatusCode, err)
		}
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	return nil
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
	var files []pullRequestFile
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

type pullRequest struct {
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Body   string `json:"body"`
	Head   struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	// Stack is set only when the pull request belongs to a stack.
	Stack *struct {
		Number int `json:"number"`
	} `json:"stack"`
}

func (p pullRequest) PullRequest() *PullRequest {
	pr := &PullRequest{
		Number: p.Number,
		URL:    p.URL,
		Body:   p.Body,
		Commit: p.Head.SHA,
		Ref:    p.Head.Ref,
		Base:   p.Base.Ref,
	}
	if p.Stack != nil {
		pr.Stack = p.Stack.Number
	}
	return pr
}

// ErrNotFound is returned when the GitHub API responds with 404 Not Found.
var ErrNotFound = errors.New("github: not found")

const (
	// stackAPIVersion is the REST API version that documents the endpoints of stacked pull requests.
	stackAPIVersion = "2026-03-10"
	// maxPages caps the number of pages a listing reads.
	maxPages = 100
)

// OpenPullRequests returns the open pull requests of the repository whose head is the
// given branch (if not empty) and whose base is the given branch (if not empty).
func (c *Client) OpenPullRequests(ctx context.Context, head, base string) ([]*PullRequest, error) {
	q := url.Values{"state": {"open"}, "per_page": {"100"}}
	if head != "" {
		owner, _, err := c.ownerRepo()
		if err != nil {
			return nil, err
		}
		q.Set("head", owner+":"+head)
	}
	if base != "" {
		q.Set("base", base)
	}
	var prs []*PullRequest
	for page := 1; ; page++ {
		if page > maxPages {
			return nil, fmt.Errorf("github: more than %d pages of open pull requests", maxPages)
		}
		q.Set("page", strconv.Itoa(page))
		var resp []pullRequest
		if err := c.get(ctx, fmt.Sprintf("/repos/%s/pulls?%s", c.repo, q.Encode()), nil, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp {
			prs = append(prs, p.PullRequest())
		}
		if len(resp) < 100 {
			return prs, nil
		}
	}
}

// Stack returns the stack of pull requests with the given number.
func (c *Client) Stack(ctx context.Context, number int) (*Stack, error) {
	var s Stack
	h := http.Header{"X-Github-Api-Version": {stackAPIVersion}}
	if err := c.get(ctx, fmt.Sprintf("/repos/%s/stacks/%d", c.repo, number), h, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// ForcePushes returns the head commits of each pull request before and after each
// force-push of its branch, oldest first, as recorded in the timeline of the pull request.
// It fails if a timeline does not hold the full history, as callers rely on it to tell
// a branch that was rewritten from one that only moved forward.
func (c *Client) ForcePushes(ctx context.Context, numbers []int) (map[int][]string, error) {
	owner, name, err := c.ownerRepo()
	if err != nil {
		return nil, err
	}
	var q strings.Builder
	q.WriteString("query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) {")
	for _, n := range numbers {
		fmt.Fprintf(&q, " pr%d: pullRequest(number: %d) { timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT], last: 100) {"+
			" pageInfo { hasPreviousPage } nodes { ... on HeadRefForcePushedEvent { beforeCommit { oid } afterCommit { oid } } } } }", n, n)
	}
	q.WriteString(" } }")
	b, err := json.Marshal(map[string]any{
		"query":     q.String(),
		"variables": map[string]string{"owner": owner, "name": name},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.graphQLURL(), bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	if b, err = io.ReadAll(res.Body); err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	type commit struct {
		OID string `json:"oid"`
	}
	var resp struct {
		Data struct {
			Repository map[string]*struct {
				TimelineItems struct {
					PageInfo struct {
						HasPreviousPage bool `json:"hasPreviousPage"`
					} `json:"pageInfo"`
					Nodes []struct {
						Before *commit `json:"beforeCommit"`
						After  *commit `json:"afterCommit"`
					} `json:"nodes"`
				} `json:"timelineItems"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &resp); err != nil {
		return nil, fmt.Errorf("unmarshalling response body: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("github: querying force-pushes: %s", resp.Errors[0].Message)
	}
	pushes := make(map[int][]string, len(numbers))
	for _, n := range numbers {
		pr := resp.Data.Repository[fmt.Sprintf("pr%d", n)]
		switch {
		case pr == nil:
			return nil, fmt.Errorf("github: pull request #%d not found", n)
		case pr.TimelineItems.PageInfo.HasPreviousPage:
			return nil, fmt.Errorf("github: pull request #%d has more force-pushes than can be read", n)
		}
		heads := []string{}
		for _, e := range pr.TimelineItems.Nodes {
			if e.Before == nil || e.After == nil || e.Before.OID == "" || e.After.OID == "" {
				return nil, fmt.Errorf("github: a force-push of pull request #%d has no commits", n)
			}
			heads = append(heads, e.Before.OID, e.After.OID)
		}
		pushes[n] = heads
	}
	return pushes, nil
}

// graphQLURL returns the URL of the GraphQL API, next to the REST API.
func (c *Client) graphQLURL() string {
	// GitHub Enterprise Server serves the REST API under /api/v3, and the GraphQL API under /api/graphql.
	if base, ok := strings.CutSuffix(strings.TrimSuffix(c.baseURL, "/"), "/api/v3"); ok {
		return base + "/api/graphql"
	}
	return strings.TrimSuffix(c.baseURL, "/") + "/graphql"
}

// get sends a GET request to the given path of the API and decodes the JSON response into v.
func (c *Client) get(ctx context.Context, path string, h http.Header, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	maps.Copy(req.Header, h)
	res, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return fmt.Errorf("%w: GET %s", ErrNotFound, path)
	default:
		return fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("unmarshalling response body: %w", err)
	}
	return nil
}

// PullRequest returns information about a pull request.
func (c *Client) PullRequest(ctx context.Context, number int) (*PullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls/%d", c.baseURL, c.repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var pr pullRequest
	if err = json.Unmarshal(b, &pr); err != nil {
		return nil, fmt.Errorf("unmarshalling response body: %w", err)
	}
	return pr.PullRequest(), nil
}

// CreatePullRequest creates a new pull request.
func (c *Client) CreatePullRequest(ctx context.Context, head, base, title, body string) (*PullRequest, error) {
	url := fmt.Sprintf("%s/repos/%s/pulls", c.baseURL, c.repo)
	j, err := json.Marshal(map[string]string{
		"head":  head,
		"base":  base,
		"title": title,
		"body":  body,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling pull request data: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(j))
	if err != nil {
		return nil, err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	var pr pullRequest
	if err = json.Unmarshal(b, &pr); err != nil {
		return nil, fmt.Errorf("unmarshalling response body: %w", err)
	}
	return pr.PullRequest(), nil
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
