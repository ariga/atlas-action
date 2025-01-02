// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type (
	Client struct {
		baseURL string
		project string
		client  *http.Client
	}
	ClientOption func(*Client) error
	Note         struct {
		ID     int    `json:"id"`
		Body   string `json:"body"`
		System bool   `json:"system"`
	}
	PrivateToken struct {
		Token string
		Base  http.RoundTripper
	}
)

const DefaultBaseURL = "https://gitlab.com/api/v4"

// WithBaseURL sets the base URL for the Gitlab client.
func WithBaseURL(url string) ClientOption {
	return func(c *Client) error {
		c.baseURL = url
		return nil
	}
}

// WithToken sets the private token for the Gitlab client.
func WithToken(token string) ClientOption {
	return func(c *Client) error {
		c.client.Transport = &PrivateToken{Token: token, Base: c.client.Transport}
		return nil
	}
}

func NewClient(project string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		baseURL: DefaultBaseURL,
		project: project,
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

func (c *Client) PullRequestNotes(ctx context.Context, prID int) ([]Note, error) {
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/notes", c.baseURL, c.project, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error querying gitlab comments with %v/%v, %w", c.project, prID, err)
	}
	defer res.Body.Close()
	buf, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading PR issue comments from %v/%v, %v", c.project, prID, err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %v when calling Gitlab API. body: %s", res.StatusCode, string(buf))
	}
	var comments []Note
	if err = json.Unmarshal(buf, &comments); err != nil {
		return nil, fmt.Errorf("error parsing gitlab notes with %v/%v from %v, %w", c.project, prID, string(buf), err)
	}
	return comments, nil
}

func (c *Client) CreateNote(ctx context.Context, prID int, comment string) error {
	body := strings.NewReader(fmt.Sprintf(`{"body":%q}`, comment))
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/notes", c.baseURL, c.project, prID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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

func (c *Client) UpdateNote(ctx context.Context, prID int, noteID int, comment string) error {
	body := strings.NewReader(fmt.Sprintf(`{"body":%q}`, comment))
	url := fmt.Sprintf("%v/projects/%v/merge_requests/%v/notes/%v", c.baseURL, c.project, prID, noteID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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

func (t *PrivateToken) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("PRIVATE-TOKEN", t.Token)
	return t.base().RoundTrip(req)
}

func (t *PrivateToken) CancelRequest(req *http.Request) {
	type canceler interface {
		CancelRequest(*http.Request)
	}
	if tr, ok := t.Base.(canceler); ok {
		tr.CancelRequest(req)
	}
}

func (t *PrivateToken) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}
