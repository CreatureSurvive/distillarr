package config

import (
	"path/filepath"
	"testing"

	"mediatrans/internal/store"
)

// a fresh install assumes nothing about paths; a stored config
// keeps every value it had.
func TestFreshDefaultsAndStoredConfigUntouched(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fresh := NewManager(st).Get()
	if len(fresh.Libraries) != 0 || !fresh.Paused || fresh.TrashDir != "" || fresh.JellyfinPathMap != "" || fresh.JellyfinURL != "" {
		t.Errorf("fresh defaults: libs=%v paused=%v trash=%q jfmap=%q jfurl=%q",
			fresh.Libraries, fresh.Paused, fresh.TrashDir, fresh.JellyfinPathMap, fresh.JellyfinURL)
	}

	old := map[string]any{
		"libraries":         []map[string]string{{"name": "movies", "path": "/srv/media/movies"}},
		"paused":            false,
		"trash_dir":         "/srv/media/.mediatrans-trash",
		"jellyfin_path_map": "/data=/srv/media",
		"jellyfin_url":      "http://192.168.1.2:8096",
	}
	if err := store.KVJSON(st, kvKey, &old); err != nil {
		t.Fatal(err)
	}
	c := NewManager(st).Get()
	if len(c.Libraries) != 1 || c.Paused || c.TrashDir != "/srv/media/.mediatrans-trash" ||
		c.JellyfinPathMap != "/data=/srv/media" || c.JellyfinURL != "http://192.168.1.2:8096" {
		t.Errorf("stored config changed: %+v", c)
	}
}
