// SPDX-License-Identifier: GPL-3.0-or-later

package replace

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRetainHardlinkAndCrossDevice(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "lib", "a.mkv")
	_ = os.MkdirAll(filepath.Dir(src), 0o755)
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(dir, ".distillarr-trash")

	// Same filesystem: an instant hardlink.
	dst := TrashPath(trash, src)
	if err := retain(src, dst, nil, false); err != nil {
		t.Fatal(err)
	}
	a, _ := os.Stat(src)
	b, _ := os.Stat(dst)
	if !os.SameFile(a, b) {
		t.Error("retention must hardlink, not copy")
	}

	// Simulated cross-device link.
	old := Link
	Link = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EXDEV} }
	t.Cleanup(func() { Link = old })

	dst2 := filepath.Join(trash, "b.mkv")
	if err := retain(src, dst2, nil, false); !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("EXDEV without allow_trash_copy: got %v", err)
	}
	if _, err := os.Stat(dst2); !os.IsNotExist(err) {
		t.Error("nothing may be copied when copying isn't allowed")
	}
	if err := retain(src, dst2, nil, true); err != nil {
		t.Fatalf("EXDEV with allow_trash_copy: %v", err)
	}
	if got, _ := os.ReadFile(dst2); string(got) != "original" {
		t.Errorf("copy content %q", got)
	}

	// Any other link failure is returned, never silently copied.
	Link = func(string, string) error { return &os.LinkError{Op: "link", Err: syscall.EPERM} }
	if err := retain(src, filepath.Join(trash, "c.mkv"), nil, true); err == nil || errors.Is(err, ErrCrossDevice) {
		t.Errorf("EPERM: got %v", err)
	}
}

func TestMountPoint(t *testing.T) {
	f := filepath.Join(t.TempDir(), "mounts")
	_ = os.WriteFile(f, []byte("overlay / overlay rw 0 0\n1:2 /srv/media fuse.mergerfs rw 0 0\n/dev/sda1 /srv/media/x ext4 rw 0 0\n/dev/sdb1 /srv/my\\040disk ext4 rw 0 0\n"), 0o644)
	old := MountsFile
	MountsFile = f
	t.Cleanup(func() { MountsFile = old })
	for path, want := range map[string]string{
		"/srv/media/movies/a.mkv": "/srv/media",
		"/srv/media/x/y":          "/srv/media/x",
		"/srv/mediaother":         "/",
		"/srv/my disk/tv":         "/srv/my disk",
	} {
		if got, _ := MountPoint(path); got != want {
			t.Errorf("%s: got %s want %s", path, got, want)
		}
	}
	if _, fs := MountPoint("/srv/media/tv"); fs != "fuse.mergerfs" {
		t.Errorf("fstype %s", fs)
	}
}
