package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeleteIssueComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/repos/owner/repo/issues/comments/123", r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)
	require.NoError(t, client.DeleteIssueComment(context.Background(), 123))
}

func TestIssueCommentBody(t *testing.T) {
	// Go-quoted, these characters are not valid JSON.
	const comment = "vertical\vtab, delete\x7f and 😀"
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var c struct {
			Body string `json:"body"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&c))
		bodies = append(bodies, c.Body)
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)
	require.NoError(t, client.CreateIssueComment(context.Background(), 1, comment))
	require.NoError(t, client.UpdateIssueComment(context.Background(), 2, comment))
	require.Equal(t, []string{comment, comment}, bodies)
}

func TestOpenPullRequests(t *testing.T) {
	var queries []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/repos/owner/repo/pulls", r.URL.Path)
		queries = append(queries, r.URL.Query())
		var prs []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			// A full page, so the next page is read.
			for i := range 100 {
				prs = append(prs, map[string]any{"number": i + 1, "head": map[string]any{"ref": "feat/a+b"}, "base": map[string]any{"ref": "main"}, "stack": nil})
			}
		case "2":
			prs = append(prs, map[string]any{
				"number":   101,
				"html_url": "https://github.com/owner/repo/pull/101",
				"head":     map[string]any{"ref": "feat/a+b", "sha": "abc"},
				"base":     map[string]any{"ref": "feat/base"},
				"stack":    map[string]any{"number": 7, "position": 2, "size": 3, "base": map[string]any{"ref": "main", "sha": "def"}},
			})
		}
		require.NoError(t, json.NewEncoder(w).Encode(prs))
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)
	prs, err := client.OpenPullRequests(context.Background(), "feat/a+b", "")
	require.NoError(t, err)
	require.Len(t, prs, 101)
	require.Equal(t, &PullRequest{Number: 1, Ref: "feat/a+b", Base: "main"}, prs[0])
	require.Equal(t, &PullRequest{Number: 101, URL: "https://github.com/owner/repo/pull/101", Commit: "abc", Ref: "feat/a+b", Base: "feat/base", Stack: 7}, prs[100])
	require.Len(t, queries, 2)
	require.Equal(t, url.Values{"state": {"open"}, "head": {"owner:feat/a+b"}, "per_page": {"100"}, "page": {"1"}}, queries[0])
	require.Equal(t, "2", queries[1].Get("page"))

	queries = nil
	_, err = client.OpenPullRequests(context.Background(), "", "main")
	require.NoError(t, err)
	require.Equal(t, url.Values{"state": {"open"}, "base": {"main"}, "per_page": {"100"}, "page": {"1"}}, queries[0])
}

func TestStack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "2026-03-10", r.Header.Get("X-GitHub-Api-Version"))
		require.Equal(t, "application/vnd.github+json", r.Header.Get("Accept"))
		switch r.URL.Path {
		case "/repos/owner/repo/stacks/4":
			// Trimmed response of GET /repos/{owner}/{repo}/stacks/{stack_number}.
			_, err := w.Write([]byte(`{
  "id": 4012585,
  "number": 4,
  "base": {"ref": "main"},
  "open": true,
  "pull_requests": [
    {"number": 1, "head": {"ref": "t0-a", "sha": "a93be79"}, "base": {"ref": "main", "sha": "b03752c"}, "state": "closed", "merged_at": "2026-11-23T20:10:58Z", "draft": false},
    {"number": 2, "head": {"ref": "t0-b", "sha": "fec76bf"}, "base": {"ref": "main", "sha": "4e76e0c"}, "state": "open", "merged_at": null, "draft": true}
  ]
}`))
			require.NoError(t, err)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)
	s, err := client.Stack(context.Background(), 4)
	require.NoError(t, err)
	require.Equal(t, 4, s.Number)
	require.True(t, s.Open)
	require.Equal(t, "main", s.Base.Ref)
	require.Len(t, s.PullRequests, 2)
	require.Equal(t, 1, s.PullRequests[0].Number)
	require.Equal(t, "closed", s.PullRequests[0].State)
	require.NotNil(t, s.PullRequests[0].MergedAt)
	require.Equal(t, "a93be79", s.PullRequests[0].Head.SHA)
	require.Equal(t, "b03752c", s.PullRequests[0].Base.SHA)
	require.Nil(t, s.PullRequests[1].MergedAt)
	require.True(t, s.PullRequests[1].Draft)
	require.Equal(t, "t0-b", s.PullRequests[1].Head.Ref)

	_, err = client.Stack(context.Background(), 5)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestForcePushes(t *testing.T) {
	var (
		body string
		resp string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/graphql", r.URL.Path)
		var req struct {
			Query     string            `json:"query"`
			Variables map[string]string `json:"variables"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, map[string]string{"owner": "owner", "name": "repo"}, req.Variables)
		body = req.Query
		_, err := w.Write([]byte(resp))
		require.NoError(t, err)
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)

	resp = `{"data":{"repository":{
		"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[
			{"beforeCommit":{"oid":"bc7fcca"},"afterCommit":{"oid":"00d2a4e"}},
			{"beforeCommit":{"oid":"00d2a4e"},"afterCommit":{"oid":"35f82a7"}}
		]}},
		"pr3":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[]}}
	}}}`
	pushes, err := client.ForcePushes(context.Background(), []int{2, 3})
	require.NoError(t, err)
	require.Equal(t, map[int][]string{2: {"bc7fcca", "00d2a4e", "00d2a4e", "35f82a7"}, 3: {}}, pushes)
	require.Contains(t, body, "pr2: pullRequest(number: 2) { timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT], last: 100)")
	require.Contains(t, body, "pr3: pullRequest(number: 3)")

	// The history must be complete.
	resp = `{"data":{"repository":{"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":true},"nodes":[]}}}}}`
	_, err = client.ForcePushes(context.Background(), []int{2})
	require.EqualError(t, err, "github: pull request #2 has more force-pushes than can be read")
	resp = `{"data":{"repository":{"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[{"beforeCommit":null,"afterCommit":{"oid":"00d2a4e"}}]}}}}}`
	_, err = client.ForcePushes(context.Background(), []int{2})
	require.EqualError(t, err, "github: a force-push of pull request #2 has no commits")
	resp = `{"data":{"repository":{"pr2":null}},"errors":[{"message":"Could not resolve to a PullRequest with the number of 2."}]}`
	_, err = client.ForcePushes(context.Background(), []int{2})
	require.EqualError(t, err, "github: querying force-pushes: Could not resolve to a PullRequest with the number of 2.")
}

func TestGraphQLURL(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.github.com":             "https://api.github.com/graphql",
		"https://api.github.com/":            "https://api.github.com/graphql",
		"https://ghe.example.com/api/v3":     "https://ghe.example.com/api/graphql",
		"https://ghe.example.com/api/v3/":    "https://ghe.example.com/api/graphql",
		"https://example.ghe.com/api/github": "https://example.ghe.com/api/github/graphql",
	} {
		c, err := NewClient("owner/repo", WithBaseURL(base))
		require.NoError(t, err)
		require.Equal(t, want, c.graphQLURL(), base)
	}
}
