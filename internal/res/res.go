// Package res classifies video resolution by its nominal class (480p,
// 576p, 720p, 1080p, 2160p). Width and height are both considered, so
// letterboxed scope films (1920×802, 3840×1600) and pillarboxed 4:3
// (1440×1080) land in the class they were mastered in, not the one their
// height alone suggests.
package res

import "fmt"

// Class returns the nominal vertical resolution for w×h.
func Class(w, h int) int {
	switch {
	case w <= 0 && h <= 0:
		return 0
	case w >= 3200 || h >= 1800:
		return 2160
	case w >= 1700 || h >= 1000:
		return 1080
	case w >= 1180 || h >= 700:
		return 720
	case h > 500 || w > 860:
		return 576
	}
	return 480
}

// Label is the human name for a class ("4K", "1080p", ...).
func Label(class int) string {
	switch class {
	case 0:
		return ""
	case 2160:
		return "4K"
	}
	return fmt.Sprintf("%dp", class)
}

// box is the 16:9 frame each class is mastered in.
func box(class int) (int, int) {
	switch class {
	case 2160:
		return 3840, 2160
	case 1080:
		return 1920, 1080
	case 720:
		return 1280, 720
	case 576:
		return 1024, 576
	}
	return 854, 480
}

// Fit returns the output size when capping w×h at class cap: the largest
// even size inside the class's frame that keeps the aspect ratio. ok is
// false when no downscale is needed (source already at or below cap).
func Fit(w, h, cap int) (ow, oh int, ok bool) {
	if cap <= 0 || w <= 0 || h <= 0 || Class(w, h) <= cap {
		return w, h, false
	}
	bw, bh := box(cap)
	scale := min(float64(bw)/float64(w), float64(bh)/float64(h))
	ow = int(float64(w)*scale) &^ 1
	oh = int(float64(h)*scale) &^ 1
	return ow, oh, true
}
