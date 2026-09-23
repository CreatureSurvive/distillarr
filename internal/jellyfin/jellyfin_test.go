package jellyfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"NowPlayingItem":{"Path":"/data/movies/Movie/Movie.mkv"},
			 "TranscodingInfo":{"IsVideoDirect":false,"HardwareAccelerationType":"qsv","TranscodeReasons":["VideoCodecNotSupported"]}},
			{"NowPlayingItem":{"Path":"/data/movies/Direct/Direct.mp4"}},
			{"PlayState":{"IsPaused":false}}
		]`))
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	sessions, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}
	if sessions[0].NowPlayingItem.Path != "/data/movies/Movie/Movie.mkv" {
		t.Errorf("wrong path: %+v", sessions[0])
	}
	if sessions[0].TranscodingInfo == nil || sessions[0].TranscodingInfo.IsVideoDirect {
		t.Errorf("expected a video transcode, got %+v", sessions[0].TranscodingInfo)
	}
	if sessions[1].TranscodingInfo != nil {
		t.Errorf("direct play must have no TranscodingInfo, got %+v", sessions[1].TranscodingInfo)
	}
	if sessions[2].NowPlayingItem != nil {
		t.Errorf("a session with nothing playing must have a nil NowPlayingItem, got %+v", sessions[2].NowPlayingItem)
	}
}
