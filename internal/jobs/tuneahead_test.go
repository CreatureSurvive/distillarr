// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"testing"
	"time"
)

// A file under a quality search is claimed by one search at a time; a
// second caller gets a channel that closes when the first one finishes.
func TestClaimTuneIsExclusive(t *testing.T) {
	e := newTestEngine(t)
	release, busy := e.claimTune("/m/a.mkv")
	if release == nil || busy != nil {
		t.Fatal("first claim should succeed")
	}
	r2, busy2 := e.claimTune("/m/a.mkv")
	if r2 != nil || busy2 == nil {
		t.Fatal("second claim on the same file should wait")
	}
	if r3, _ := e.claimTune("/m/b.mkv"); r3 == nil {
		t.Fatal("another file is independent")
	} else {
		r3()
	}
	select {
	case <-busy2:
		t.Fatal("closed before release")
	default:
	}
	release()
	select {
	case <-busy2:
	case <-time.After(time.Second):
		t.Fatal("waiter not woken on release")
	}
	if r4, _ := e.claimTune("/m/a.mkv"); r4 == nil {
		t.Fatal("file should be claimable again")
	}
}
