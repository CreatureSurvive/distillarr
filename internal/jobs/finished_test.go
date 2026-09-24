// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"sync"
	"testing"
	"time"
)

func TestOnFinishedFanOut(t *testing.T) {
	e := &Engine{}
	var mu sync.Mutex
	var got []int64
	var wg sync.WaitGroup

	// A panicking subscriber must not stop the others.
	wg.Add(1)
	e.OnFinished(func(ev ReplacedEvent) {
		defer wg.Done()
		panic("boom")
	})
	wg.Add(1)
	e.OnFinished(func(ev ReplacedEvent) {
		defer wg.Done()
		mu.Lock()
		got = append(got, ev.JobID)
		mu.Unlock()
	})
	wg.Add(1)
	e.OnFinished(func(ev ReplacedEvent) {
		defer wg.Done()
		mu.Lock()
		got = append(got, ev.JobID*10)
		mu.Unlock()
	})

	e.fireFinished(ReplacedEvent{JobID: 7, Kind: "encode"})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("subscribers did not all run within timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %v, want two successful subscribers to have run", got)
	}
	sum := got[0] + got[1]
	if sum != 77 { // 7 + 70, in either order
		t.Fatalf("got %v, want values summing to 77", got)
	}
}

func TestOnFinishedNoSubscribers(t *testing.T) {
	e := &Engine{}
	// Must not panic or block with nothing registered.
	e.fireFinished(ReplacedEvent{JobID: 1})
}
