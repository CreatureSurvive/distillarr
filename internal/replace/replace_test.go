package replace

import (
	"os"
	"path/filepath"
	"testing"
)

// A healthy upscale sits within a couple of levels of its source; the corrupt
// Vulkan readback this guards against returns zero chroma (~128 away).
func TestChromaClose(t *testing.T) {
	if !chromaClose(123.7, 126.4, 122.0, 129.0) {
		t.Error("a healthy upscale differs from its source by a few levels")
	}
	if !chromaClose(123.7, 126.4, 105.0, 140.0) {
		t.Error("a scene change between the two decodes must not be mistaken for corruption")
	}
	if chromaClose(123.7, 126.4, 0, 0) {
		t.Error("zero chroma (solid green) must be rejected")
	}
	if chromaClose(123.7, 126.4, 123.7, 60) {
		t.Error("one collapsed channel is enough to reject")
	}
}

func TestAddCopyNeverClobbers(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "Movie.mp4")
	temp := filepath.Join(dir, ".mediatrans-1.mp4.tmp")
	dest := filepath.Join(dir, "Movie - 1080p upscale.mp4")
	os.WriteFile(src, []byte("original"), 0o644)
	os.WriteFile(temp, []byte("upscaled"), 0o644)

	if err := AddCopy(temp, dest, &SrcStat{Mode: 0o664}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "upscaled" {
		t.Errorf("copy content: %q", b)
	}
	if b, _ := os.ReadFile(src); string(b) != "original" {
		t.Error("the original must be untouched")
	}
	if _, err := os.Stat(temp); !os.IsNotExist(err) {
		t.Error("the temp file must be gone")
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o664 {
		t.Errorf("source permissions should carry over, got %v", fi.Mode().Perm())
	}

	// A second run must refuse rather than overwrite what is there.
	os.WriteFile(temp, []byte("second"), 0o644)
	if err := AddCopy(temp, dest, nil); err == nil {
		t.Error("an existing destination must be an error")
	}
	if b, _ := os.ReadFile(dest); string(b) != "upscaled" {
		t.Errorf("the existing copy was overwritten: %q", b)
	}
	if _, err := os.Stat(temp); err != nil {
		t.Error("a refused install must leave the temp file for cleanup, not delete it")
	}
}
