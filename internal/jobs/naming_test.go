// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsStaleTempName(t *testing.T) {
	for name, want := range map[string]bool{
		".distillarr-12.mp4.tmp": true,
		".mediatrans-12.mp4.tmp": true, // existing installs' temps must still be swept
		".distillarr-12.mp4":     false,
		"movie.mp4":              false,
		".distillarr-manual":     false, // a directory name, not a temp file
	} {
		if got := isStaleTempName(name); got != want {
			t.Errorf("isStaleTempName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestManualDirFor(t *testing.T) {
	dir := t.TempDir()
	// No manual-hold directory yet: new installs (and this one) get the
	// new name.
	if got := manualDirFor(dir); got != manualDirNew {
		t.Errorf("no existing dir: got %q, want %q", got, manualDirNew)
	}

	// An existing legacy directory must keep being used, so one folder's
	// held files are never split across both names.
	if err := os.MkdirAll(filepath.Join(dir, manualDirLegacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := manualDirFor(dir); got != manualDirLegacy {
		t.Errorf("existing legacy dir: got %q, want %q", got, manualDirLegacy)
	}
}

func TestMoveToManualUsesExistingLegacyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, manualDirLegacy), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "orphan.mp4.tmp")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	moveToManual(src)
	if _, err := os.Stat(filepath.Join(dir, manualDirLegacy, "orphan.mp4.tmp")); err != nil {
		t.Errorf("expected the file moved into the existing legacy dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, manualDirNew)); err == nil {
		t.Error("a new manual dir must not be created alongside an existing legacy one")
	}
}

func TestMoveToManualCreatesNewDirWhenNoneExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "orphan.mp4.tmp")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	moveToManual(src)
	if _, err := os.Stat(filepath.Join(dir, manualDirNew, "orphan.mp4.tmp")); err != nil {
		t.Errorf("expected the file moved into the new manual dir: %v", err)
	}
}
