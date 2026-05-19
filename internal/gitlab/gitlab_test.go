package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeleteNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodDelete, r.Method)
		require.Equal(t, "/projects/1/merge_requests/2/notes/3", r.URL.Path)
		require.Equal(t, "token", r.Header.Get("PRIVATE-TOKEN"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client, err := NewClient("1", WithBaseURL(srv.URL), WithToken("token"))
	require.NoError(t, err)
	require.NoError(t, client.DeleteNote(context.Background(), 2, 3))
}
