package azuredevops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestGetPullRequest(t *testing.T) {
	t.Run("successful request", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expectedPath := "/myorg/myproject/_apis/git/repositories/myrepo/pullrequests/123"
			require.Equal(t, "GET", r.Method)
			require.Contains(t, r.URL.Path, expectedPath)
			pr := PullRequest{
				ID:    123,
				Title: "Test Pull Request",
			}
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(pr))
		}))
		defer srv.Close()
		// Create client with mock server URL
		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = srv.URL
		// Test GetPullRequest
		ctx := context.Background()
		pr, err := client.PullRequest(ctx, 123)
		require.NoError(t, err)
		require.NotNil(t, pr)
		require.Equal(t, 123, pr.ID)
		require.Equal(t, "Test Pull Request", pr.Title)
	})
}

func TestClient(t *testing.T) {
	t.Run("end-to-end with authentication", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			require.Equal(t, "Bearer test-token", auth)
			pr := PullRequest{
				ID:    456,
				Title: "Authenticated Request",
			}
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(pr))
		}))
		defer srv.Close()
		// Create client with token
		token := &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
		}
		client, err := NewClient("myorg", "myproject", "myrepo", WithToken(token))
		require.NoError(t, err)
		client.baseURL = srv.URL
		pr, err := client.PullRequest(context.Background(), 456)
		require.NoError(t, err)
		require.NotNil(t, pr)
		require.Equal(t, 456, pr.ID)
		require.Equal(t, "Authenticated Request", pr.Title)
	})
}

func TestComment(t *testing.T) {
	t.Run("comment and reply", func(t *testing.T) {
		threadCreated := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			require.Equal(t, "Bearer test-token", auth)
			if strings.Contains(r.URL.Path, "/threads") && !strings.Contains(r.URL.Path, "/comments") {
				require.Equal(t, "POST", r.Method)
				threadCreated = true
				thread := CommentThread{
					ID: 456,
					Comments: []Comment{
						{
							ID:      789,
							Content: "Initial comment",
							Author: struct {
								DisplayName string `json:"displayName"`
							}{DisplayName: "Test User"},
						},
					},
					Status: "active",
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				require.NoError(t, json.NewEncoder(w).Encode(thread))
			} else if strings.Contains(r.URL.Path, "/threads/456/comments") {
				// Adding comment to thread
				require.Equal(t, "POST", r.Method)
				require.True(t, threadCreated, "Thread should be created before adding comment")
				comment := Comment{
					ID:      790,
					Content: "Reply comment",
					Author: struct {
						DisplayName string `json:"displayName"`
					}{DisplayName: "Test User"},
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				require.NoError(t, json.NewEncoder(w).Encode(comment))
			} else {
				t.Errorf("Unexpected request to %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		// Create client with token
		token := &oauth2.Token{
			AccessToken: "test-token",
			TokenType:   "Bearer",
		}
		client, err := NewClient("myorg", "myproject", "myrepo", WithToken(token))
		require.NoError(t, err)
		client.baseURL = srv.URL
		ctx := context.Background()
		// First, create a comment thread
		thread, err := client.AddComment(ctx, 123, "Initial comment")
		require.NoError(t, err)
		require.NotNil(t, thread)
		require.Equal(t, 456, thread.ID)
		require.Len(t, thread.Comments, 1)
		require.Equal(t, "Initial comment", thread.Comments[0].Content)
		// Then, add a reply to the thread
		comment, err := client.AddCommentToThread(ctx, 123, 456, "Reply comment")
		require.NoError(t, err)
		require.NotNil(t, comment)
		require.Equal(t, 790, comment.ID)
		require.Equal(t, "Reply comment", comment.Content)
	})
}

func TestUpdateComment(t *testing.T) {
	t.Run("successful comment update", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expectedPath := "/myorg/myproject/_apis/git/repositories/myrepo/pullrequests/123/threads/456/comments/789"
			require.Equal(t, "PATCH", r.Method)
			require.Contains(t, r.URL.Path, expectedPath)
			require.Contains(t, r.URL.RawQuery, "api-version=7.1-preview.1")
			require.Equal(t, "application/json", r.Header.Get("Content-Type"))
			var reqBody UpdateCommentRequest
			err := json.NewDecoder(r.Body).Decode(&reqBody)
			require.NoError(t, err)
			require.Equal(t, "Updated comment content", reqBody.Content)
			comment := Comment{
				ID:      789,
				Content: "Updated comment content",
				Author: struct {
					DisplayName string `json:"displayName"`
				}{DisplayName: "Test User"},
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(comment))
		}))
		defer srv.Close()
		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = srv.URL
		ctx := context.Background()
		comment, err := client.UpdateComment(ctx, 123, 456, 789, "Updated comment content")
		require.NoError(t, err)
		require.NotNil(t, comment)
		require.Equal(t, 789, comment.ID)
		require.Equal(t, "Updated comment content", comment.Content)
		require.Equal(t, "Test User", comment.Author.DisplayName)
	})
}

func TestGetCommentThread(t *testing.T) {
	t.Run("successful thread retrieval", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			expectedPath := "/myorg/myproject/_apis/git/repositories/myrepo/pullrequests/123/threads/456"
			require.Equal(t, "GET", r.Method)
			require.Contains(t, r.URL.Path, expectedPath)
			require.Contains(t, r.URL.RawQuery, "api-version=7.1-preview.1")
			thread := CommentThread{
				ID: 456,
				Comments: []Comment{
					{
						ID:      789,
						Content: "First comment",
						Author: struct {
							DisplayName string `json:"displayName"`
						}{DisplayName: "Test User"},
					},
					{
						ID:      790,
						Content: "Second comment",
						Author: struct {
							DisplayName string `json:"displayName"`
						}{DisplayName: "Another User"},
					},
				},
				Status: "active",
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			require.NoError(t, json.NewEncoder(w).Encode(thread))
		}))
		defer srv.Close()
		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = srv.URL
		thread, err := client.GetCommentThread(context.Background(), 123, 456)
		require.NoError(t, err)
		require.NotNil(t, thread)
		require.Equal(t, 456, thread.ID)
		require.Len(t, thread.Comments, 2)
		require.Equal(t, 789, thread.Comments[0].ID)
		require.Equal(t, "First comment", thread.Comments[0].Content)
		require.Equal(t, 790, thread.Comments[1].ID)
		require.Equal(t, "Second comment", thread.Comments[1].Content)
		require.Equal(t, "active", thread.Status)
	})
	t.Run("thread not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": "Thread not found"}`))
		}))
		defer server.Close()
		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = server.URL
		thread, err := client.GetCommentThread(context.Background(), 123, 999)
		require.Error(t, err)
		require.Nil(t, thread)
		require.Contains(t, err.Error(), "unexpected status code 404")
		require.Contains(t, err.Error(), `"error": "Thread not found"`)
	})
}

func TestUpdateFirstComment(t *testing.T) {
	t.Run("successful first comment update", func(t *testing.T) {
		getThreadCalled := false
		updateCommentCalled := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" && strings.Contains(r.URL.Path, "/threads/456") && !strings.Contains(r.URL.Path, "/comments") {
				getThreadCalled = true
				thread := CommentThread{
					ID: 456,
					Comments: []Comment{
						{
							ID:      789,
							Content: "Original first comment",
							Author: struct {
								DisplayName string `json:"displayName"`
							}{DisplayName: "Test User"},
						},
						{
							ID:      790,
							Content: "Second comment",
							Author: struct {
								DisplayName string `json:"displayName"`
							}{DisplayName: "Another User"},
						},
					},
					Status: "active",
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(thread))
			} else if r.Method == "PATCH" && strings.Contains(r.URL.Path, "/comments/789") {
				updateCommentCalled = true
				var reqBody UpdateCommentRequest
				err := json.NewDecoder(r.Body).Decode(&reqBody)
				require.NoError(t, err)
				require.Equal(t, "Updated first comment", reqBody.Content)
				comment := Comment{
					ID:      789,
					Content: "Updated first comment",
					Author: struct {
						DisplayName string `json:"displayName"`
					}{DisplayName: "Test User"},
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(comment))
			} else {
				t.Errorf("Unexpected request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = srv.URL
		ctx := context.Background()
		comment, err := client.UpdateFirstComment(ctx, 123, 456, "Updated first comment")
		require.NoError(t, err)
		require.NotNil(t, comment)
		require.Equal(t, 789, comment.ID)
		require.Equal(t, "Updated first comment", comment.Content)
		require.True(t, getThreadCalled, "Should have called get thread")
		require.True(t, updateCommentCalled, "Should have called update comment")
	})

	t.Run("thread has no comments", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			thread := CommentThread{
				ID:       456,
				Comments: []Comment{}, // Empty comments
				Status:   "active",
			}
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(thread))
		}))
		defer srv.Close()
		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = srv.URL
		ctx := context.Background()
		comment, err := client.UpdateFirstComment(ctx, 123, 456, "Updated content")
		require.Error(t, err)
		require.Nil(t, comment)
		require.Contains(t, err.Error(), "thread 456 has no comments to update")
	})

	t.Run("thread not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error": "Thread not found"}`))
		}))
		defer srv.Close()

		client, err := NewClient("myorg", "myproject", "myrepo")
		require.NoError(t, err)
		client.baseURL = srv.URL

		ctx := context.Background()
		comment, err := client.UpdateFirstComment(ctx, 123, 999, "Updated content")
		require.Error(t, err)
		require.Nil(t, comment)
		require.Contains(t, err.Error(), "getting thread to update first comment")
	})
}
