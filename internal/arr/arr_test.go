package arr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTestAndRootFolders(t *testing.T) {
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		switch r.URL.Path {
		case "/api/v3/system/status":
			json.NewEncoder(w).Encode(Status{Version: "4.0.20.3014", AppName: "Sonarr"})
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]RootFolder{
				{ID: 1, Path: "/data/tvshows", Accessible: true, FreeSpace: 100},
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "test-key", Sonarr)
	st, err := c.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "4.0.20.3014" || st.AppName != "Sonarr" {
		t.Errorf("got %+v", st)
	}
	if gotKey != "test-key" {
		t.Errorf("X-Api-Key = %q, want test-key", gotKey)
	}

	rf, err := c.RootFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rf) != 1 || rf[0].Path != "/data/tvshows" || !rf[0].Accessible {
		t.Errorf("got %+v", rf)
	}
}

func TestUnconfiguredClientErrors(t *testing.T) {
	var c *Client
	if _, err := c.Test(context.Background()); err == nil {
		t.Error("nil client should error, not panic or succeed")
	}
	c = New("", "", Sonarr)
	if _, err := c.Test(context.Background()); err == nil {
		t.Error("empty base/key should error")
	}
}

func TestAuthKeyRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"message":"Unauthorized"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "wrong-key", Radarr)
	_, err := c.Test(context.Background())
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if got := FriendlyError(err); got == err.Error() {
		t.Errorf("FriendlyError should rewrite a 401 into something readable, got %q", got)
	}
}

func TestFriendlyErrorNil(t *testing.T) {
	if got := FriendlyError(nil); got != "" {
		t.Errorf("FriendlyError(nil) = %q, want empty", got)
	}
}
