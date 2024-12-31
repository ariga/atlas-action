package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

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
