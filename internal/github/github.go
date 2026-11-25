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
	"regexp"
	"slices"
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

// maxForcePushPages caps the number of pages of force-pushes read for each pull request.
const maxForcePushPages = 50

// ForcePushes returns the head commits of each pull request before and after each
// force-push of its branch, oldest first, as recorded in the timeline of the pull request.
// A pull request whose timeline cannot be read in full is left out of the result, as
// callers rely on the history to tell a branch that was rewritten from one that only
// moved forward. The timeline starts when the pull request was opened.
func (c *Client) ForcePushes(ctx context.Context, numbers []int) (map[int][]string, error) {
	owner, name, err := c.ownerRepo()
	if err != nil {
		return nil, err
	}
	var (
		pushes  = make(map[int][]string, len(numbers))
		cursors = make(map[int]string, len(numbers)) // Cursor of the oldest page read so far.
		pending = slices.Clone(numbers)
	)
	// The pages are read from the newest, for all the pull requests at once.
	for page := 0; len(pending) > 0 && page < maxForcePushPages; page++ {
		prs, err := c.forcePushPage(ctx, owner, name, pending, cursors)
		if err != nil {
			return nil, err
		}
		var next []int
		for _, n := range pending {
			pr := prs[fmt.Sprintf("pr%d", n)]
			if pr == nil {
				delete(pushes, n)
				continue
			}
			heads := make([]string, 0, 2*len(pr.TimelineItems.Nodes))
			for _, e := range pr.TimelineItems.Nodes {
				// A force-push whose commits are gone cannot tell the heads of the branch.
				if e.Before == nil || e.After == nil || e.Before.OID == "" || e.After.OID == "" {
					heads = nil
					break
				}
				heads = append(heads, e.Before.OID, e.After.OID)
			}
			if heads == nil {
				delete(pushes, n)
				continue
			}
			pushes[n] = append(heads, pushes[n]...)
			if info := pr.TimelineItems.PageInfo; info.HasPreviousPage {
				if info.StartCursor == "" {
					delete(pushes, n)
					continue
				}
				cursors[n] = info.StartCursor
				next = append(next, n)
			}
		}
		pending = next
	}
	// Pull requests with more pages than read.
	for _, n := range pending {
		delete(pushes, n)
	}
	return pushes, nil
}

type (
	// forcePushes is a page of the force-push events of a pull request.
	forcePushes struct {
		TimelineItems struct {
			PageInfo struct {
				HasPreviousPage bool   `json:"hasPreviousPage"`
				StartCursor     string `json:"startCursor"`
			} `json:"pageInfo"`
			Nodes []struct {
				Before *struct {
					OID string `json:"oid"`
				} `json:"beforeCommit"`
				After *struct {
					OID string `json:"oid"`
				} `json:"afterCommit"`
			} `json:"nodes"`
		} `json:"timelineItems"`
	}
)

// forcePushPage reads a page of force-pushes for each of the pull requests: the newest page,
// or the page before the cursor of the pull request.
func (c *Client) forcePushPage(ctx context.Context, owner, name string, numbers []int, cursors map[int]string) (map[string]*forcePushes, error) {
	var (
		q    strings.Builder
		vars = map[string]any{"owner": owner, "name": name}
		decl = []string{"$owner: String!", "$name: String!"}
	)
	for _, n := range numbers {
		before := ""
		if cur, ok := cursors[n]; ok {
			v := fmt.Sprintf("c%d", n)
			vars[v], before = cur, ", before: $"+v
			decl = append(decl, "$"+v+": String!")
		}
		fmt.Fprintf(&q, " pr%d: pullRequest(number: %d) { timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT], last: 100%s) {"+
			" pageInfo { hasPreviousPage startCursor } nodes { ... on HeadRefForcePushedEvent { beforeCommit { oid } afterCommit { oid } } } } }", n, n, before)
	}
	b, err := json.Marshal(map[string]any{
		"query":     fmt.Sprintf("query(%s) { repository(owner: $owner, name: $name) {%s } }", strings.Join(decl, ", "), q.String()),
		"variables": vars,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Repository map[string]*forcePushes `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := c.do(ctx, http.MethodPost, c.graphQLURL(), b, nil, &resp); err != nil {
		return nil, err
	}
	// A pull request that cannot be read is null, with an error. Others are still reported.
	if resp.Data.Repository == nil {
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("github: querying force-pushes: %s", resp.Errors[0].Message)
		}
		return nil, errors.New("github: querying force-pushes: no repository in the response")
	}
	return resp.Data.Repository, nil
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
	_, err := c.do(ctx, http.MethodGet, c.baseURL+path, nil, h, v)
	return err
}

// do sends a request to the given URL of the API, with the JSON body if not nil, decodes
// the JSON response into v, and returns the headers of the response.
func (c *Client) do(ctx context.Context, method, u string, body []byte, h http.Header, v any) (http.Header, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	maps.Copy(req.Header, h)
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error calling GitHub API: %w", err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s %s", ErrNotFound, method, strings.TrimPrefix(u, c.baseURL))
	default:
		return nil, fmt.Errorf("unexpected status code %v: with body: %v", res.StatusCode, string(b))
	}
	if err := json.Unmarshal(b, v); err != nil {
		return nil, fmt.Errorf("unmarshalling response body: %w", err)
	}
	return res.Header, nil
}

// reNextLink matches the URL of the next page in a Link header.
var reNextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// nextPage returns the URL of the next page from the Link header of a response, or an empty
// string if there is none. It fails if the URL is outside of the API, as the client sends the
// token with every request.
func (c *Client) nextPage(h http.Header) (string, error) {
	for _, l := range h.Values("Link") {
		if m := reNextLink.FindStringSubmatch(l); m != nil {
			if !strings.HasPrefix(m[1], strings.TrimSuffix(c.baseURL, "/")+"/") {
				return "", fmt.Errorf("github: the next page %q is outside of the API", m[1])
			}
			return m[1], nil
		}
	}
	return "", nil
}

// maxActivityPages caps the number of pages of activities read for a branch.
const maxActivityPages = 30

// BranchHeads returns the commits the branch pointed to before and after each of its changes,
// as GitHub records them in the activity of the repository: its pushes, force-pushes, merges,
// creation and deletion. Unlike the timeline of a pull request, the activity also records the
// changes from before the pull request of the branch was opened. complete reports whether the
// activity goes back to the creation of the branch, and so holds every head the branch had.
func (c *Client) BranchHeads(ctx context.Context, branch string) (heads []string, complete bool, _ error) {
	q := url.Values{"ref": {"refs/heads/" + branch}, "direction": {"desc"}, "per_page": {"100"}}
	var (
		next = fmt.Sprintf("%s/repos/%s/activity?%s", c.baseURL, c.repo, q.Encode())
		seen = make(map[string]bool)
		last string // Type of the oldest activity read.
	)
	for page := 0; next != ""; page++ {
		if page == maxActivityPages {
			return heads, false, nil
		}
		var acts []struct {
			Before string `json:"before"`
			After  string `json:"after"`
			Type   string `json:"activity_type"`
		}
		h, err := c.do(ctx, http.MethodGet, next, nil, nil, &acts)
		if err != nil {
			return nil, false, err
		}
		for _, a := range acts {
			// The creation and deletion of a branch have no commit on one side.
			for _, c := range []string{a.After, a.Before} {
				if strings.Trim(c, "0") != "" && !seen[c] {
					seen[c] = true
					heads = append(heads, c)
				}
			}
			last = a.Type
		}
		if next, err = c.nextPage(h); err != nil {
			return nil, false, err
		}
	}
	return heads, last == "branch_creation", nil
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
