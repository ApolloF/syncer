package gdrive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRevokeReportsFailures(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.FormValue("token"); got != "refresh-tok" {
			t.Errorf("revoked token %q, want the refresh token", got)
		}
		w.WriteHeader(status)
	}))
	old := revokeURL
	revokeURL = srv.URL
	t.Cleanup(func() { revokeURL = old })
	tok := Token{Refresh: "refresh-tok", Access: "access-tok"}

	for _, c := range []struct {
		status int
		ok     bool
	}{{http.StatusOK, true}, {http.StatusBadRequest, true}, {http.StatusInternalServerError, false}} {
		status = c.status
		if err := Revoke(context.Background(), tok); (err == nil) != c.ok {
			t.Errorf("status %d: err = %v", c.status, err)
		}
	}

	srv.Close()
	if Revoke(context.Background(), tok) == nil {
		t.Error("unreachable Google reported as revoked")
	}
}
