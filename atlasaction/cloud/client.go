package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/vektah/gqlparser/gqlerror"
)

// cloudURL holds Atlas Cloud API URL.
const cloudURL = "https://api.atlasgo.cloud/query"

type (
	// Client is a client for the Atlas Cloud API.
	Client struct {
		client   *retryablehttp.Client
		endpoint string
	}
	// roundTripper is a http.RoundTripper that adds the Authorization header.
	roundTripper struct {
		token, version, cliVersion string
	}
)

// RoundTrip implements http.RoundTripper.
func (r *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("User-Agent", fmt.Sprintf("Atlas Action/%s Atlas CLI/%s", r.version, r.cliVersion))
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultTransport.RoundTrip(req)
}

func newClient(endpoint, token, version, cliVersion string) *Client {
	if endpoint == "" {
		endpoint = cloudURL
	}
	client := retryablehttp.NewClient()
	client.HTTPClient.Timeout = time.Second * 60
	client.HTTPClient.Transport = &roundTripper{
		token:      token,
		version:    version,
		cliVersion: cliVersion,
	}
	return &Client{
		endpoint: endpoint,
		client:   client,
	}
}

// New creates a new Client for the Atlas Cloud API.
func New(token, version, cliVersion string) *Client {
	return newClient("", token, version, cliVersion)
}

type (
	ScopeIdent struct {
		URL     string   `json:"url"`               // The (Atlas CLI) URL of the database snapshot.
		ExtID   string   `json:"extID,omitempty"`   // Optional user defined identifier for an instance.
		Schemas []string `json:"schemas,omitempty"` // List of schemas to inspect, empty to inspect everything.
		Exclude []string `json:"exclude,omitempty"` // List of exclude patterns to apply on the inspection.
	}
	PushSnapshotStatsInput struct {
		Stats string    `json:"stats"` // JSON-encoded statistics about the snapshot.
		Time  time.Time `json:"time"`  // Time of the snapshot.
	}
	PushSnapshotInput struct {
		ScopeIdent

		Snapshot  *SnapshotInput          `json:"snapshot,omitempty"` // The snapshot taken, required if hashMatch is false.
		HashMatch bool                    `json:"hashMatch"`          // If hash of snapshot matches hash of last snapshot.
		Stats     *PushSnapshotStatsInput `json:"stats,omitempty"`    // Statistics about the snapshot.
	}
	SnapshotInput struct {
		Hash string `json:"hash"` // Atlas schema hash for the given HCL.
		HCL  string `json:"hcl"`  // HCL representation of the snapshot.
	}
)

// PushSnapshot to the cloud, return the url to the schema snapshot in the cloud.
func (c *Client) PushSnapshot(ctx context.Context, input *PushSnapshotInput) (string, error) {
	var (
		req = `mutation pushSnapshot($input: PushSnapshotInput!) {
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
			Input *PushSnapshotInput `json:"input"`
		}{
			Input: input,
		}
	)
	if err := c.post(ctx, req, vars, &payload); err != nil {
		return "", err
	}
	return payload.PushSnapshot.URL, nil
}

type SnapshotHashInput struct{ ScopeIdent }

// SnapshotHash retrieves the hash of the schema snapshot from the cloud.
func (c *Client) SnapshotHash(ctx context.Context, input *SnapshotHashInput) (string, error) {
	var (
		req = `query snapshotHash($input: SnapshotHashInput!) {
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
			Input *SnapshotHashInput `json:"input"`
		}{
			Input: input,
		}
	)
	if err := c.post(ctx, req, vars, &payload); err != nil {
		return "", err
	}
	return payload.SnapshotHash.Hash, nil
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
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
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
