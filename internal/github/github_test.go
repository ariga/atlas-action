package github

import (
	"context"
	"encoding/json"
	"fmt"
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
	require.Equal(t, "t0-b", s.PullRequests[1].Head.Ref)

	_, err = client.Stack(context.Background(), 5)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestForcePushes(t *testing.T) {
	type request struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	var (
		reqs  []request
		resps []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/graphql", r.URL.Path)
		var req request
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "owner", req.Variables["owner"])
		require.Equal(t, "repo", req.Variables["name"])
		reqs = append(reqs, req)
		require.NotEmpty(t, resps, "unexpected request: %s", req.Query)
		_, err := w.Write([]byte(resps[0]))
		resps = resps[1:]
		require.NoError(t, err)
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)

	resps = []string{`{"data":{"repository":{
		"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[
			{"beforeCommit":{"oid":"bc7fcca"},"afterCommit":{"oid":"00d2a4e"}},
			{"beforeCommit":{"oid":"00d2a4e"},"afterCommit":{"oid":"35f82a7"}}
		]}},
		"pr3":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[]}}
	}}}`}
	pushes, err := client.ForcePushes(context.Background(), []int{2, 3})
	require.NoError(t, err)
	require.Equal(t, map[int][]string{2: {"bc7fcca", "00d2a4e", "00d2a4e", "35f82a7"}, 3: {}}, pushes)
	require.Contains(t, reqs[0].Query, "query($owner: String!, $name: String!) {")
	require.Contains(t, reqs[0].Query, "pr2: pullRequest(number: 2) { timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT], last: 100) {")
	require.Contains(t, reqs[0].Query, "pr3: pullRequest(number: 3)")

	// Older pages are read for the pull requests that have them, and only for them.
	reqs, resps = nil, []string{
		`{"data":{"repository":{
			"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":true,"startCursor":"Y3Vyc29yOjI="},"nodes":[
				{"beforeCommit":{"oid":"c"},"afterCommit":{"oid":"d"}}
			]}},
			"pr3":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[
				{"beforeCommit":{"oid":"x"},"afterCommit":{"oid":"y"}}
			]}}
		}}}`,
		`{"data":{"repository":{
			"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":false,"startCursor":"Y3Vyc29yOjE="},"nodes":[
				{"beforeCommit":{"oid":"a"},"afterCommit":{"oid":"b"}}
			]}}
		}}}`,
	}
	pushes, err = client.ForcePushes(context.Background(), []int{2, 3})
	require.NoError(t, err)
	require.Equal(t, map[int][]string{2: {"a", "b", "c", "d"}, 3: {"x", "y"}}, pushes)
	require.Len(t, reqs, 2)
	require.Contains(t, reqs[1].Query, "query($owner: String!, $name: String!, $c2: String!) {")
	require.Contains(t, reqs[1].Query, "pr2: pullRequest(number: 2) { timelineItems(itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT], last: 100, before: $c2) {")
	require.NotContains(t, reqs[1].Query, "pr3")
	require.Equal(t, "Y3Vyc29yOjI=", reqs[1].Variables["c2"])

	// A pull request whose history cannot be read is left out, and the others are kept.
	reqs, resps = nil, []string{`{"data":{"repository":{
		"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[{"beforeCommit":null,"afterCommit":{"oid":"00d2a4e"}}]}},
		"pr3":null,
		"pr4":{"timelineItems":{"pageInfo":{"hasPreviousPage":true},"nodes":[]}},
		"pr5":{"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[{"beforeCommit":{"oid":"a"},"afterCommit":{"oid":"b"}}]}}
	}},"errors":[{"message":"Could not resolve to a PullRequest with the number of 3."}]}`}
	pushes, err = client.ForcePushes(context.Background(), []int{2, 3, 4, 5})
	require.NoError(t, err)
	require.Equal(t, map[int][]string{5: {"a", "b"}}, pushes)

	// More pages than can be read.
	page := `{"data":{"repository":{"pr2":{"timelineItems":{"pageInfo":{"hasPreviousPage":true,"startCursor":"c"},"nodes":[{"beforeCommit":{"oid":"a"},"afterCommit":{"oid":"b"}}]}}}}}`
	reqs, resps = nil, nil
	for range maxForcePushPages {
		resps = append(resps, page)
	}
	pushes, err = client.ForcePushes(context.Background(), []int{2})
	require.NoError(t, err)
	require.Empty(t, pushes)
	require.Len(t, reqs, maxForcePushPages)

	// The request fails as a whole.
	resps = []string{`{"data":null,"errors":[{"message":"API rate limit exceeded"}]}`}
	_, err = client.ForcePushes(context.Background(), []int{2})
	require.EqualError(t, err, "github: querying force-pushes: API rate limit exceeded")
}

func TestBranchHeads(t *testing.T) {
	var (
		queries []url.Values
		pages   map[string]string // Body of each page, by its cursor.
		next    map[string]string // Link header of each page, by its cursor.
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		if r.URL.Path != "/repos/owner/repo/activity" && r.URL.Path != "/repositories/1/activity" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		queries = append(queries, r.URL.Query())
		cur := r.URL.Query().Get("after")
		if l := next[cur]; l != "" {
			w.Header().Set("Link", l)
		}
		_, err := w.Write([]byte(pages[cur]))
		require.NoError(t, err)
	}))
	defer srv.Close()
	client, err := NewClient("owner/repo", WithBaseURL(srv.URL))
	require.NoError(t, err)

	// Two pages, newest first, back to the creation of the branch.
	pages = map[string]string{
		"": `[
			{"activity_type":"force_push","ref":"refs/heads/feat/a","before":"c3","after":"c4"},
			{"activity_type":"push","ref":"refs/heads/feat/a","before":"c2","after":"c3"}
		]`,
		"p2": `[
			{"activity_type":"force_push","ref":"refs/heads/feat/a","before":"c1","after":"c2"},
			{"activity_type":"branch_creation","ref":"refs/heads/feat/a","before":"0000000000000000000000000000000000000000","after":"c1"}
		]`,
	}
	next = map[string]string{
		"": fmt.Sprintf(`<%s/repositories/1/activity?after=p2&per_page=100>; rel="next"`, srv.URL),
	}
	heads, complete, err := client.BranchHeads(context.Background(), "feat/a")
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, []string{"c4", "c3", "c2", "c1"}, heads)
	require.Equal(t, url.Values{"ref": {"refs/heads/feat/a"}, "direction": {"desc"}, "per_page": {"100"}}, queries[0])
	require.Len(t, queries, 2)

	// The activity does not go back to the creation of the branch, like after a retention period.
	pages[""] = `[{"activity_type":"force_push","before":"c3","after":"c4"},{"activity_type":"push","before":"c2","after":"c3"}]`
	next = nil
	heads, complete, err = client.BranchHeads(context.Background(), "feat/a")
	require.NoError(t, err)
	require.False(t, complete)
	require.Equal(t, []string{"c4", "c3", "c2"}, heads)

	// A deleted branch, created again.
	pages[""] = `[
		{"activity_type":"branch_creation","before":"0000000000000000000000000000000000000000","after":"c9"},
		{"activity_type":"branch_deletion","before":"c4","after":"0000000000000000000000000000000000000000"},
		{"activity_type":"branch_creation","before":"0000000000000000000000000000000000000000","after":"c4"}
	]`
	heads, complete, err = client.BranchHeads(context.Background(), "feat/a")
	require.NoError(t, err)
	require.True(t, complete)
	require.Equal(t, []string{"c9", "c4"}, heads)

	// More pages than read.
	queries = nil
	pages = map[string]string{"": `[{"activity_type":"push","before":"c1","after":"c2"}]`, "p": `[{"activity_type":"push","before":"c1","after":"c2"}]`}
	next = map[string]string{
		"":  fmt.Sprintf(`<%s/repositories/1/activity?after=p>; rel="next"`, srv.URL),
		"p": fmt.Sprintf(`<%s/repositories/1/activity?after=p>; rel="next"`, srv.URL),
	}
	_, complete, err = client.BranchHeads(context.Background(), "feat/a")
	require.NoError(t, err)
	require.False(t, complete)
	require.Len(t, queries, maxActivityPages)

	// A next page outside of the API is not followed, as the token is sent with every request.
	next = map[string]string{"": `<https://example.com/activity?after=p>; rel="next"`}
	_, _, err = client.BranchHeads(context.Background(), "feat/a")
	require.EqualError(t, err, `github: the next page "https://example.com/activity?after=p" is outside of the API`)

	// The endpoint is not available.
	client, err = NewClient("owner/other", WithBaseURL(srv.URL))
	require.NoError(t, err)
	_, _, err = client.BranchHeads(context.Background(), "feat/a")
	require.ErrorIs(t, err, ErrNotFound)
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
