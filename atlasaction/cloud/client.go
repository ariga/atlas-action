package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vektah/gqlparser/gqlerror"
)

// cloudURL holds Atlas Cloud API URL.
const cloudURL = "https://api.atlasgo.cloud/query"

type (
	// Client is a client for the Atlas Cloud API.
	Client struct {
		client   *http.Client
		endpoint string
		token    string
	}
	// roundTripper is a http.RoundTripper that adds the Authorization header.
	roundTripper struct {
		token string
	}
)

// RoundTrip implements http.RoundTripper.
func (r *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("User-Agent", "atlas-action")
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultTransport.RoundTrip(req)
}

// New creates a new Client for the Atlas Cloud API.
func New(token string) *Client {
	return &Client{
		endpoint: cloudURL,
		client: &http.Client{
			Transport: &roundTripper{
				token: token,
			},
			Timeout: time.Second * 30,
		},
		token: token,
	}
}

// sends a POST request to the Atlas Cloud API.
func (c *Client) post(ctx context.Context, query string, vars, data any) error {
	body, err := json.Marshal(struct {
		Query     string `json:"query"`
		Variables any    `json:"variables,omitempty"`
	}{
		Query:     query,
		Variables: vars,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	res, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer req.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", res.StatusCode)
	}
	var scan = struct {
		Data   any           `json:"data"`
		Errors gqlerror.List `json:"errors,omitempty"`
	}{
		Data: data,
	}
	if err := json.NewDecoder(res.Body).Decode(&scan); err != nil {
		return err
	}
	if len(scan.Errors) > 0 {
		return scan.Errors
	}
	return nil
}

// ValidateToken validates the token inside the client with the Atlas Cloud API.
func (c *Client) ValidateToken(ctx context.Context) error {
	var (
		payload struct {
			ValidateToken struct {
				Success bool `json:"success"`
			} `json:"validateToken"`
		}
		query = `mutation validateToken($token: String!) {
		validateToken(token: $token) {
			success
		}
	}`
		vars = struct {
			Token string `json:"token"`
		}{
			Token: c.token,
		}
	)
	return c.post(ctx, query, vars, &payload)
}

type PushSnapshotInput struct {
	URL       string   `json:"url"`               // The (Atlas CLI) URL of the database a snapshot is taken of.
	HCL       string   `json:"hcl"`               // HCL representation of the snapshot. If hashMatch is false, this is required.
	HashMatch bool     `json:"hashMatch"`         // Whether the hash of the taken snapshot matches the hash of the last snapshot.
	ExtID     string   `json:"extID,omitempty"`   // Optional externally defined ID to identify an instance if the URL is ambiguous, e.g. in case of localhost connections.
	Schemas   []string `json:"schemas,omitempty"` // List of schemas to inspect. Empty if all schemas should be inspected.
	Exclude   []string `json:"exclude,omitempty"` // List of exclude patterns to apply on the inspection.
}

// PushSnapshot to the cloud, return the url to the schema snapshot in the cloud.
func (c *Client) PushSnapshot(ctx context.Context, input PushSnapshotInput) (string, error) {
	var (
		req = `mutation ($input: PushSnapshotInput!) {
			pushSnapshot(input: $input) {
				newVersion
				url
			}
		}`
		payload struct {
			PushSnapshot struct {
				NewVersion bool   `json:"newVersion"`
				URL        string `json:"url"`
			} `json:"pushSnapshot"`
		}
		vars = struct {
			Input PushSnapshotInput `json:"input"`
		}{
			Input: input,
		}
	)
	if err := c.post(ctx, req, vars, &payload); err != nil {
		return "", err
	}
	return payload.PushSnapshot.URL, nil
}

type SnapshotHashInput struct {
	ExtID   string   `json:"extID,omitempty"`   // Optional externally defined ID to identify an instance if the URL is ambiguous, e.g. in case of localhost connections.
	URL     string   `json:"url"`               // The (Atlas CLI) URL of the database a snapshot is taken of.
	Schemas []string `json:"schemas,omitempty"` // List of schemas to inspect. Empty if all schemas should be inspected.
	Exclude []string `json:"exclude,omitempty"` // List of exclude patterns to apply on the inspection.
}

// SnapshotHash retrieves the hash of the schema snapshot from the cloud.
func (c *Client) SnapshotHash(ctx context.Context, input SnapshotHashInput) (string, error) {
	var (
		req = `query ($input: SnapshotHashInput!) {
			snapshotHash(input: $input) {
				hash
			}
		}`
		payload struct {
			SnapshotHash struct {
				Hash string `json:"hash"`
			} `json:"snapshotHash"`
		}
		vars = struct {
			Input SnapshotHashInput `json:"input"`
		}{
			Input: input,
		}
	)
	if err := c.post(ctx, req, vars, &payload); err != nil {
		return "", err
	}
	return payload.SnapshotHash.Hash, nil
}
