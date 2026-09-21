package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Hub fans out events to SSE subscribers. Slow consumers are dropped
// (the client re-syncs on reconnect).
type Hub struct {
	mu   sync.Mutex
	subs map[uint64]chan []byte
	next uint64
}

func NewHub() *Hub {
	return &Hub{subs: map[uint64]chan []byte{}}
}

type sseEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
	At    string `json:"at"`
}

// Broadcast sends an event to every subscriber.
func (h *Hub) Broadcast(event string, data any) {
	b, err := json.Marshal(sseEvent{Event: event, Data: data, At: time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		select {
		case ch <- b:
		default: // full buffer: drop the subscriber
			close(ch)
			delete(h.subs, id)
		}
	}
}

// ServeHTTP implements the /api/v1/events endpoint.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.next++
	id := h.next
	h.subs[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			close(c)
			delete(h.subs, id)
		}
	}()

	// initial hello
	fmt.Fprint(w, "event: hello\ndata: {\"ok\":true}\n\n")
	fl.Flush()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case b, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
