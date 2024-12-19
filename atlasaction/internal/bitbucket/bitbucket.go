// Copyright 2021-present The Atlas Authors. All rights reserved.
// This source code is licensed under the Apache 2.0 license found
// in the LICENSE file in the root directory of this source tree.

package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

type (
	// Client is the Bitbucket client.
	Client struct {
		baseURL   string
		workspace string
		repoSlug  string
		client    *http.Client
	}
	// CommitReport is a report for a commit.
	CommitReport struct {
		Title             string     `json:"title"`
		Details           string     `json:"details"`
		ReportType        ReportType `json:"report_type"`
		ExternalID        string     `json:"external_id,omitempty"`
		Reporter          string     `json:"reporter,omitempty"`
		Link              string     `json:"link,omitempty"`
		RemoteLinkEnabled bool       `json:"remote_link_enabled,omitempty"`
		LogoURL           string     `json:"logo_url,omitempty"`
		Result            Result     `json:"result,omitempty"`
		// Map of data to be reported.
		// Maximum of 10 data points.
		Data []ReportData `json:"data,omitempty"`
	}
	// ReportType: SECURITY, COVERAGE, TEST, BUG
	ReportType string
	// Result: PASSED, FAILED, PENDING, SKIPPED, IGNORED
	Result string
	// Severity: LOW, MEDIUM, HIGH, CRITICAL
	Severity string
	// ReportData is the data to be reported.
	ReportData struct {
		Title string `json:"title"`
		Value any    `json:"value"`
		Type  string `json:"type,omitempty"`
	}
	// ReportAnnotation is the annotation to be reported.
	ReportAnnotation struct {
		AnnotationType AnnotationType `json:"annotation_type"`
		Summary        string         `json:"summary"`
		Result         Result         `json:"result,omitempty"`
		Severity       Severity       `json:"severity,omitempty"`
		ExternalID     string         `json:"external_id,omitempty"`
		Path           string         `json:"path,omitempty"`
		Line           int            `json:"line,omitempty"`
		Details        string         `json:"details,omitempty"`
		Link           string         `json:"link,omitempty"`
	}
	// AnnotationType: VULNERABILITY, CODE_SMELL, BUG
	AnnotationType string
	// PullRequestComment is a comment.
	PullRequestComment struct {
		Content Rendered `json:"content"`
		ID      int      `json:"id,omitempty"`
	}
	Rendered struct {
		Raw    string `json:"raw"`
		Markup string `json:"markup,omitempty"`
		Html   string `json:"html,omitempty"`
	}
	// PaginatedResponse is a paginated response.
	PaginatedResponse[T any] struct {
		Size     int    `json:"size"`
		Page     int    `json:"page"`
		PageLen  int    `json:"pagelen"`
		Next     string `json:"next"`
		Previous string `json:"previous"`
		Values   []T    `json:"values"`
	}
	// Error is an API error.
	Error struct {
		ID      string              `json:"id"`
		Message string              `json:"message"`
		Detail  string              `json:"detail"`
		Fields  map[string][]string `json:"fields"`
		Data    map[string]string   `json:"data"`
	}
	// ClientOption is the option when creating a new client.
	ClientOption func(*Client) error
)

// ReportType values.
const (
	ReportTypeSecurity ReportType = "SECURITY"
	ReportTypeCoverage ReportType = "COVERAGE"
	ReportTypeTest     ReportType = "TEST"
	ReportTypeBug      ReportType = "BUG"
)

// Result values.
const (
	ResultPassed  Result = "PASSED"
	ResultFailed  Result = "FAILED"
	ResultPending Result = "PENDING"
	ResultSkipped Result = "SKIPPED"
	ResultIgnored Result = "IGNORED"
)

// Severity values.
const (
	SeverityLow      Severity = "LOW"
	SeverityMedium   Severity = "MEDIUM"
	SeverityHigh     Severity = "HIGH"
	SeverityCritical Severity = "CRITICAL"
)

// AnnotationType values.
const (
	AnnotationTypeVulnerability AnnotationType = "VULNERABILITY"
	AnnotationTypeCodeSmell     AnnotationType = "CODE_SMELL"
	AnnotationTypeBug           AnnotationType = "BUG"
)

// DefaultBaseURL is the default base URL for the Bitbucket API.
const DefaultBaseURL = "https://api.bitbucket.org/2.0"

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

// WithProxy returns a ClientOption that sets the proxy for the client.
func WithProxy(proxyFn func() (*url.URL, error)) ClientOption {
	return func(c *Client) error {
		proxy, err := proxyFn()
		if err != nil {
			return err
		}
		c.client.Transport = &http.Transport{
			Proxy: http.ProxyURL(proxy),
		}
		u, err := url.Parse(c.baseURL)
		if err != nil {
			return err
		}
		// Set the scheme to the proxy scheme.
		u.Scheme = proxy.Scheme
		c.baseURL = u.String()
		return nil
	}
}

// NewClient returns a new Bitbucket client.
func NewClient(workspace, repoSlug string, opts ...ClientOption) (*Client, error) {
	c := &Client{
		workspace: workspace,
		repoSlug:  repoSlug,
		baseURL:   DefaultBaseURL,
		client:    &http.Client{Timeout: time.Second * 30},
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// CreateReport creates a commit report for the given commit.
func (b *Client) CreateReport(ctx context.Context, commit string, r *CommitReport) (*CommitReport, error) {
	if len(r.Data) > 10 {
		return nil, fmt.Errorf("bitbucket: maximum of 10 data points allowed")
	}
	u, err := b.repoURL("commit", commit, "reports", r.ExternalID)
	if err != nil {
		return nil, err
	}
	res, err := b.json(ctx, http.MethodPut, u, r)
	if err != nil {
		return nil, err
	}
	return responseDecode[CommitReport](res, http.StatusOK)
}

func (b *Client) CreateReportAnnotations(ctx context.Context, commit, reportID string, annotations []ReportAnnotation) ([]ReportAnnotation, error) {
	u, err := b.repoURL("commit", commit, "reports", reportID, "annotations")
	if err != nil {
		return nil, err
	}
	res, err := b.json(ctx, http.MethodPost, u, annotations)
	if err != nil {
		return nil, err
	}
	a, err := responseDecode[[]ReportAnnotation](res, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return *a, nil
}

// PullRequestComments returns the comments of a pull request.
func (b *Client) PullRequestComments(ctx context.Context, prID int) (result []PullRequestComment, err error) {
	u, err := b.repoURL("pullrequests", strconv.Itoa(prID), "comments")
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
		list, err := responseDecode[PaginatedResponse[PullRequestComment]](res, http.StatusOK)
		if err != nil {
			return nil, err
		}
		result = append(result, list.Values...)
		u = list.Next // Fetch the next page if available.
	}
	return result, nil
}

// PullRequestCreateComment creates a comment on a pull request.
func (b *Client) PullRequestCreateComment(ctx context.Context, prID int, raw string) (*PullRequestComment, error) {
	u, err := b.repoURL("pullrequests", strconv.Itoa(prID), "comments")
	if err != nil {
		return nil, err
	}
	res, err := b.json(ctx, http.MethodPost, u, map[string]any{
		"content": map[string]string{
			"raw": raw,
		},
	})
	if err != nil {
		return nil, err
	}
	return responseDecode[PullRequestComment](res, http.StatusCreated)
}

// PullRequestUpdateComment updates a comment on a pull request.
func (b *Client) PullRequestUpdateComment(ctx context.Context, prID, id int, raw string) (*PullRequestComment, error) {
	u, err := b.repoURL("pullrequests", strconv.Itoa(prID), "comments", strconv.Itoa(id))
	if err != nil {
		return nil, err
	}
	res, err := b.json(ctx, http.MethodPut, u, map[string]any{
		"content": map[string]string{
			"raw": raw,
		},
	})
	if err != nil {
		return nil, err
	}
	return responseDecode[PullRequestComment](res, http.StatusOK)
}

func (b *Client) repoURL(elems ...string) (string, error) {
	return url.JoinPath(b.baseURL, append([]string{"repositories", b.workspace, b.repoSlug}, elems...)...)
}

// json sends a JSON request to the Bitbucket API.
func (b *Client) json(ctx context.Context, method, u string, data any) (*http.Response, error) {
	d, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(d))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return b.client.Do(req)
}

// responseDecode decodes the response body
// if the status code is the expected status.
// otherwise, it decodes the body as an error.
func responseDecode[T any](r *http.Response, s int) (*T, error) {
	defer r.Body.Close()
	d := json.NewDecoder(r.Body)
	if r.StatusCode != s {
		var res struct {
			Type  string `json:"type"` // always "error"
			Error Error  `json:"error"`
		}
		if err := d.Decode(&res); err != nil {
			return nil, fmt.Errorf("bitbucket: failed to decode error response: %w", err)
		}
		return nil, &res.Error
	}
	var res T
	if err := d.Decode(&res); err != nil {
		return nil, fmt.Errorf("bitbucket: failed to decode response: %w", err)
	}
	return &res, nil
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("bitbucket: %s: %s", e.Message, e.Detail)
	}
	return fmt.Sprintf("bitbucket: %s", e.Message)
}

// AddText adds a text data to the commit report.
func (r *CommitReport) AddText(title, value string) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "TEXT",
		Value: value,
	})
}

// AddBoolean adds a boolean data to the commit report.
func (r *CommitReport) AddBoolean(title string, value bool) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "BOOLEAN",
		Value: value,
	})
}

// AddNumber adds a number data to the commit report.
func (r *CommitReport) AddNumber(title string, value int64) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "NUMBER",
		Value: value,
	})
}

// AddPercentage adds a percentage data to the commit report.
func (r *CommitReport) AddPercentage(title string, value float64) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "PERCENTAGE",
		Value: value,
	})
}

// AddDate adds a date data to the commit report.
func (r *CommitReport) AddDate(title string, value time.Time) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "DATE",
		Value: value.UnixMilli(),
	})
}

// AddDuration adds a duration data to the commit report.
func (r *CommitReport) AddDuration(title string, value time.Duration) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "DURATION",
		Value: value.Milliseconds(),
	})
}

// AddLink adds a link data to the commit report.
func (r *CommitReport) AddLink(title string, text string, u *url.URL) {
	r.Data = append(r.Data, ReportData{
		Title: title, Type: "LINK",
		Value: map[string]string{
			"text": text, "href": u.String(),
		},
	})
}

var _ error = (*Error)(nil)
