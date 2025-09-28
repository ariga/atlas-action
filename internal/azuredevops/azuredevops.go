package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

type (
	// Client is the Azure DevOps client.
	Client struct {
		org     string
		project string
		repo    string
		client  *http.Client
		baseURL string
	}
	// PullRequest is the Azure DevOps pull request.
	PullRequest struct {
		ID    int    `json:"pullRequestId"`
		Title string `json:"title"`
	}
	// Comment is the Azure DevOps comment.
	Comment struct {
		ID     int `json:"id"`
		Author struct {
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Content string `json:"content"`
	}
	// CommentThread represents a comment thread in Azure DevOps.
	CommentThread struct {
		ID       int       `json:"id"`
		Comments []Comment `json:"comments"`
		Status   string    `json:"status"`
	}
	// CreateCommentRequest represents the request body for creating a comment.
	CreateCommentRequest struct {
		Content string `json:"content"`
	}
	// CreateCommentThreadRequest represents the request body for creating a comment thread.
	CreateCommentThreadRequest struct {
		Comments []CreateCommentRequest `json:"comments"`
		Status   string                 `json:"status"`
	}
	// UpdateCommentRequest represents the request body for updating a comment.
	UpdateCommentRequest struct {
		Content string `json:"content"`
	}
	// ClientOption is the option when creating a new client.
	ClientOption func(*Client) error
)

// DefaultBaseURL is the default base URL for the Azure DevOps API.
const DefaultBaseURL = "https://dev.azure.com"

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

// WithBaseURL returns a ClientOption that sets the base URL for the client.
func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) error {
		c.baseURL = baseURL
		return nil
	}
}

// NewClient returns a new Azure DevOps client.
func NewClient(org, project, repo string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		org:     org,
		project: project,
		repo:    repo,
		client:  &http.Client{Timeout: time.Second * 30},
		baseURL: DefaultBaseURL,
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

func (c *Client) PullRequest(ctx context.Context, prID int) (*PullRequest, error) {
	url := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d?api-version=7.1-preview.1", c.baseURL, c.org, c.project, c.repo, prID)
	res, err := c.json(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading azure devops pull request with %v/%v, %w", c.repo, prID, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling Azure DevOps API. body: %s", res.StatusCode, string(buf))
	}
	var pullRequest PullRequest
	if err = json.Unmarshal(buf, &pullRequest); err != nil {
		return nil, fmt.Errorf("parsing azure devops pull request with %v/%v, %w", c.repo, prID, err)
	}
	return &pullRequest, nil
}

// AddComment adds a comment to a pull request by creating a new comment thread.
func (c *Client) AddComment(ctx context.Context, prID int, content string) (*CommentThread, error) {
	url := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads?api-version=7.1-preview.1",
		c.baseURL, c.org, c.project, c.repo, prID)
	reqBody := CreateCommentThreadRequest{
		Comments: []CreateCommentRequest{
			{Content: content},
		},
		Status: "active",
	}
	res, err := c.json(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response for comment creation: %w", err)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected status code %d when creating comment. body: %s", res.StatusCode, string(buf))
	}
	var thread CommentThread
	if err = json.Unmarshal(buf, &thread); err != nil {
		return nil, fmt.Errorf("parsing comment thread response: %w", err)
	}
	return &thread, nil
}

// AddCommentToThread adds a comment to an existing comment thread.
func (c *Client) AddCommentToThread(ctx context.Context, prID, threadID int, content string) (*Comment, error) {
	url := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads/%d/comments?api-version=7.1-preview.1",
		c.baseURL, c.org, c.project, c.repo, prID, threadID)
	reqBody := CreateCommentRequest{
		Content: content,
	}
	res, err := c.json(ctx, http.MethodPost, url, reqBody)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response for comment creation: %w", err)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("unexpected status code %d when adding comment to thread. body: %s", res.StatusCode, string(buf))
	}
	var comment Comment
	if err = json.Unmarshal(buf, &comment); err != nil {
		return nil, fmt.Errorf("parsing comment response: %w", err)
	}
	return &comment, nil
}

// GetCommentThread retrieves a comment thread from a pull request.
func (c *Client) GetCommentThread(ctx context.Context, prID, threadID int) (*CommentThread, error) {
	url := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads/%d?api-version=7.1-preview.1",
		c.baseURL, c.org, c.project, c.repo, prID, threadID)
	res, err := c.json(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("getting comment thread %d for pull request %d: %w", threadID, prID, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response for comment thread retrieval: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d when getting comment thread. body: %s", res.StatusCode, string(buf))
	}
	var thread CommentThread
	if err = json.Unmarshal(buf, &thread); err != nil {
		return nil, fmt.Errorf("parsing comment thread response: %w", err)
	}
	return &thread, nil
}

// UpdateComment updates an existing comment in a pull request thread.
func (c *Client) UpdateComment(ctx context.Context, prID, threadID, commentID int, content string) (*Comment, error) {
	url := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads/%d/comments/%d?api-version=7.1-preview.1",
		c.baseURL, c.org, c.project, c.repo, prID, threadID, commentID)
	reqBody := UpdateCommentRequest{
		Content: content,
	}
	res, err := c.json(ctx, http.MethodPatch, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("updating comment %d in thread %d for pull request %d: %w", commentID, threadID, prID, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response for comment update: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d when updating comment. body: %s", res.StatusCode, string(buf))
	}
	var comment Comment
	if err = json.Unmarshal(buf, &comment); err != nil {
		return nil, fmt.Errorf("parsing updated comment response: %w", err)
	}
	return &comment, nil
}

// UpdateFirstComment updates the first comment in a comment thread.
func (c *Client) UpdateFirstComment(ctx context.Context, prID, threadID int, content string) (*Comment, error) {
	// First, get the thread to find the first comment ID
	thread, err := c.GetCommentThread(ctx, prID, threadID)
	if err != nil {
		return nil, fmt.Errorf("getting thread to update first comment: %w", err)
	}
	if len(thread.Comments) == 0 {
		return nil, fmt.Errorf("thread %d has no comments to update", threadID)
	}
	firstCommentID := thread.Comments[0].ID
	return c.UpdateComment(ctx, prID, threadID, firstCommentID, content)
}

// ListCommentThreads retrieves all comment threads for a pull request.
func (c *Client) ListCommentThreads(ctx context.Context, prID int) ([]CommentThread, error) {
	url := fmt.Sprintf("%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d/threads?api-version=7.1-preview.1",
		c.baseURL, c.org, c.project, c.repo, prID)
	res, err := c.json(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("listing comment threads for pull request %d: %w", prID, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response for comment threads list: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d when listing comment threads. body: %s", res.StatusCode, string(buf))
	}
	var response struct {
		Value []CommentThread `json:"value"`
	}
	if err = json.Unmarshal(buf, &response); err != nil {
		return nil, fmt.Errorf("parsing comment threads list response: %w", err)
	}
	return response.Value, nil
}

// json sends a JSON request to the Azure DevOps API.
func (c *Client) json(ctx context.Context, method, u string, data any) (*http.Response, error) {
	d, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(d))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.client.Do(req)
}
