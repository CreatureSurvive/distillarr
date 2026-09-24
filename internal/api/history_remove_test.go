// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestRemoveHistoryUnder(t *testing.T) {
	s := newTestServer(t)
	job := func(path, status string) int64 {
		j := &store.Job{SrcPath: path, Priority: 100000, Backend: "sw", Codec: "hevc", MaxAttempts: 1}
		if err := s.st.CreateJob(j); err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			_ = s.st.FinishJob(j.ID, status, 0, "", "")
		}
		return j.ID
	}
	job("/m/.test/movies/a.mkv", store.StatusDone)
	job("/m/.test/movies/b.mkv", store.StatusFailed)
	job("/m/.test/movies/c.mkv", "queued")          // active: kept
	kept := job("/m/.test/movies/d.mkv", store.StatusDone)
	_ = s.st.AddTrash(store.TrashItem{OrigPath: "/m/.test/movies/d.mkv", TrashPath: "/t/d", JobID: kept}) // restorable: kept
	job("/m/.test_other/x.mkv", store.StatusDone) // sibling prefix: kept
	job("/m/movies/real.mkv", store.StatusDone)   // real: kept

	post := func(body string) string {
		w := httptest.NewRecorder()
		s.removeHistoryUnder(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		return w.Body.String()
	}
	if got := post(`{"path":"/m/.test/"}`); !strings.Contains(got, `"matches":2`) {
		t.Fatalf("dry run: %s", got)
	}
	if got := post(`{"path":"/m/.test","dry_run":false}`); !strings.Contains(got, `"cleared":2`) {
		t.Fatalf("remove: %s", got)
	}
	counts, _ := s.st.CountJobsByStatus()
	if counts["done"] != 3 || counts["queued"] != 1 || counts["failed"] != 0 {
		t.Errorf("left %v", counts)
	}
	if got := post(`{"path":"/"}`); !strings.Contains(got, "absolute folder") {
		t.Errorf("root must be refused: %s", got)
	}
}
