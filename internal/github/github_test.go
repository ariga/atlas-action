package github

import (
	"context"
	"net/http"
	"net/http/httptest"
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
