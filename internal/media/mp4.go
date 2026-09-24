// SPDX-License-Identifier: GPL-3.0-or-later

package media

import (
	"fmt"
	"os"
)

// MoovFirst walks top-level MP4 boxes and reports whether the index
// (moov) comes before the media data (mdat): "faststart", so playback and
// streaming can begin without reading the whole file.
func MoovFirst(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var off int64
	hdr := make([]byte, 16)
	for i := 0; i < 64; i++ {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return false, fmt.Errorf("mp4 box walk: %w", err)
		}
		size := int64(uint32(hdr[0])<<24 | uint32(hdr[1])<<16 | uint32(hdr[2])<<8 | uint32(hdr[3]))
		typ := string(hdr[4:8])
		if size == 1 {
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return false, err
			}
			size = 0
			for _, b := range hdr[8:16] {
				size = size<<8 | int64(b)
			}
		}
		switch typ {
		case "moov":
			return true, nil
		case "mdat":
			return false, nil
		}
		if size < 8 {
			return false, fmt.Errorf("mp4 box walk: bad size for %q", typ)
		}
		off += size
	}
	return false, fmt.Errorf("mp4 box walk: no moov found")
}
