// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Trash items carry their file's show/season so the UI can group them,
// and a whole group can be deleted in one call that survives a bad id.
func TestTrashGroupedAndBulkDelete(t *testing.T) {
	s := newTestServer(t)
	dir := t.TempDir()
	var ids []int64
	for ep := 1; ep <= 2; ep++ {
		cur := filepath.Join(dir, fmt.Sprintf("Show S01E%02d.mp4", ep))
		f := &store.File{Path: cur, Library: "tvshows", Title: "Show", Season: 1, Episode: ep, Size: 10}
		if err := s.st.UpsertFile(f, nil); err != nil {
			t.Fatal(err)
		}
		tp := filepath.Join(dir, "trash", filepath.Base(cur))
		_ = os.MkdirAll(filepath.Dir(tp), 0o755)
		if err := os.WriteFile(tp, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Original was an .mkv; the file row now lives at the .mp4 path.
		_ = s.st.AddTrash(store.TrashItem{OrigPath: strings.TrimSuffix(cur, ".mp4") + ".mkv", TrashPath: tp, CurrentPath: cur, Size: 5})
	}
	items, err := s.st.ListTrash()
	if err != nil || len(items) != 2 {
		t.Fatalf("ListTrash: %v %+v", err, items)
	}
	for _, it := range items {
		if it.Library != "tvshows" || it.Title != "Show" || it.Season != 1 || it.Episode == 0 {
			t.Errorf("item not linked to its file: %+v", it)
		}
		ids = append(ids, it.ID)
	}

	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"ids":[%d,%d,99999]}`, ids[0], ids[1])
	s.deleteTrashBulk(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if got := w.Body.String(); !strings.Contains(got, `"count":2`) || !strings.Contains(got, `"failed":1`) {
		t.Fatalf("bulk delete: %s", got)
	}
	if left, _ := s.st.ListTrash(); len(left) != 0 {
		t.Errorf("left %+v", left)
	}
	for _, it := range items {
		if _, err := os.Stat(it.TrashPath); !os.IsNotExist(err) {
			t.Errorf("%s not removed: %v", it.TrashPath, err)
		}
	}

	w = httptest.NewRecorder()
	s.deleteTrashBulk(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"ids":[]}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty ids: %d", w.Code)
	}
}
