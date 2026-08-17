package cloud

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// drain reads the request body. A handler that blocks without it never sees its
// context canceled: the server only starts watching the connection for a close
// once the request body hits EOF.
func drain(t *testing.T, r *http.Request) {
	t.Helper()
	_, err := io.Copy(io.Discard, r.Body)
	require.NoError(t, err)
}

func TestClient_Retry(t *testing.T) {
	var (
		ctx   = context.Background()
		calls = []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusOK}
		srv   = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			code := calls[0]
			w.WriteHeader(code)
			calls = calls[1:]
			if code != http.StatusOK {
				return
			}
			_, err := fmt.Fprint(w, `{"data":{"snapshotHash":{"hash":"hash"}}}`)
			require.NoError(t, err)
		}))
		client = newClient(srv.URL, "token", "version", "cliVersion")
	)
	defer srv.Close()
	_, err := client.SnapshotHash(ctx, &SnapshotHashInput{})
	require.NoError(t, err)
	require.Empty(t, calls)
}

func TestClient_RequestTimeout(t *testing.T) {
	client := newClient("", "token", "version", "cliVersion")
	// The timeout must be on the http.Client, which recomputes its deadline on
	// every Do call, so it bounds each attempt rather than the whole retry loop.
	require.Equal(t, 5*time.Minute, client.client.HTTPClient.Timeout)
	// And the roundTripper must wrap the client's transport rather than replace
	// it, otherwise the request never reaches the pooled transport.
	rt, ok := client.client.HTTPClient.Transport.(*roundTripper)
	require.True(t, ok)
	require.NotNil(t, rt.base)
}

// Each attempt gets the full timeout, and an attempt that hangs is cut off at it
// instead of running until the caller gives up.
func TestClient_RequestTimeout_Hangs(t *testing.T) {
	const timeout = 250 * time.Millisecond
	var (
		ctx = context.Background()
		mu  sync.Mutex
		// Duration of every attempt as observed by the server.
		attempts []time.Duration
		srv      = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			start := time.Now()
			drain(t, r)
			<-r.Context().Done() // Never respond.
			mu.Lock()
			attempts = append(attempts, time.Since(start))
			mu.Unlock()
		}))
		client = newClient(srv.URL, "token", "version", "cliVersion")
	)
	defer srv.Close()
	client.client.HTTPClient.Timeout = timeout
	// Keep the retry loop itself short, it is not what is under test.
	client.client.RetryMax = 2
	client.client.RetryWaitMin = time.Millisecond
	client.client.RetryWaitMax = time.Millisecond
	client.client.Logger = nil

	_, err := client.SnapshotHash(ctx, &SnapshotHashInput{})
	require.ErrorContains(t, err, "giving up after 3 attempt(s)")
	// The client returns as soon as it cuts an attempt off, slightly before the
	// handler it abandoned records itself. Close waits for those handlers.
	srv.Close()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, attempts, 3, "every attempt must be bounded and retried")
	for i, d := range attempts {
		require.Greater(t, d, timeout/2, "attempt %d was cut off before its timeout", i)
		require.Less(t, d, 10*timeout, "attempt %d outlived its timeout", i)
	}
}

// The timeout covers reading the response body, not just the headers.
func TestClient_RequestTimeout_HangingBody(t *testing.T) {
	const timeout = 250 * time.Millisecond
	var (
		ctx = context.Background()
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			drain(t, r)
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done() // Headers only, never a body.
		}))
		client = newClient(srv.URL, "token", "version", "cliVersion")
	)
	defer srv.Close()
	client.client.HTTPClient.Timeout = timeout
	client.client.RetryMax = 0
	client.client.Logger = nil

	start := time.Now()
	_, err := client.SnapshotHash(ctx, &SnapshotHashInput{})
	// Whether the transport or the http.Client notices the expired deadline first
	// decides which of the two timeout errors surfaces, so match on the behavior.
	var timedOut interface{ Timeout() bool }
	require.ErrorAs(t, err, &timedOut)
	require.True(t, timedOut.Timeout(), "got %v", err)
	require.Less(t, time.Since(start), 10*timeout)
}

// A caller context that is done must stop the client, timeout or not.
func TestClient_ContextCanceled(t *testing.T) {
	var (
		srv = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			drain(t, r)
			<-r.Context().Done()
		}))
		client = newClient(srv.URL, "token", "version", "cliVersion")
	)
	defer srv.Close()
	client.client.Logger = nil
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.SnapshotHash(ctx, &SnapshotHashInput{})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

// The roundTripper must not mutate the request it is handed, and its headers
// must still reach the server on every attempt.
func TestClient_RoundTripper(t *testing.T) {
	var (
		ctx  = context.Background()
		hdrs []http.Header
		srv  = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hdrs = append(hdrs, r.Header.Clone())
			if len(hdrs) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, err := fmt.Fprint(w, `{"data":{"snapshotHash":{"hash":"hash"}}}`)
			require.NoError(t, err)
		}))
		client = newClient(srv.URL, "token", "version", "cliVersion")
	)
	defer srv.Close()
	client.client.RetryWaitMin = time.Millisecond
	client.client.RetryWaitMax = time.Millisecond
	client.client.Logger = nil

	_, err := client.SnapshotHash(ctx, &SnapshotHashInput{})
	require.NoError(t, err)
	require.Len(t, hdrs, 2)
	for i, h := range hdrs {
		require.Equal(t, "Bearer token", h.Get("Authorization"), "attempt %d", i)
		require.Equal(t, "Atlas Action/version Atlas CLI/cliVersion", h.Get("User-Agent"), "attempt %d", i)
		require.Equal(t, "application/json", h.Get("Content-Type"), "attempt %d", i)
	}
}
