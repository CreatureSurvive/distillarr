// Package notify delivers event messages to any number of notification
// targets (shoutrrr URLs: Discord, ntfy, Pushover, email, ...).
//
// Send never blocks the caller: events go to an in-process queue. Every
// Window the buffered events are flushed per target: more than
// CoalesceAfter events of one key become a single summary message, info
// events inside the target's quiet hours wait until the window ends, and
// each target's own worker delivers with retries. Target URLs are
// secrets: they're read from config at delivery time and never logged.
package notify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"mediatrans/internal/config"
)

// Levels, lowest first.
const (
	Info    = "info"
	Warning = "warning"
	Error   = "error"
)

func levelRank(l string) int {
	switch l {
	case Warning:
		return 1
	case Error:
		return 2
	}
	return 0
}

// Event is one thing worth telling someone about.
type Event struct {
	Key   string // event key, matched against Notifier.Events
	Level string // info|warning|error
	Title string
	Body  string
	// Link is an app-relative link ("#/file/12"); PublicURL is prepended
	// when set, otherwise it's left out (a relative link is useless in a
	// chat message).
	Link string
	// Group is the plural summary used when events of this key coalesce,
	// e.g. "jobs finished" gives "12 jobs finished". Blank falls back to
	// "12 × <first title>".
	Group string
}

// SendFunc delivers one message to one target URL.
type SendFunc func(url, title, body string) error

// Tunables, overridable in tests.
var (
	Window        = 60 * time.Second
	CoalesceAfter = 3
	RetryBackoff  = 2 * time.Second
	Tries         = 3
)

// Status is a target's last delivery outcome, for the settings UI.
type Status struct {
	LastSentAt  string `json:"last_sent_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
}

type message struct{ title, body string }

// Service is the notifier: queue, per-target buffers and workers.
type Service struct {
	cfg  func() config.Config
	send SendFunc
	now  func() time.Time

	in chan Event

	mu      sync.Mutex
	pending map[string][]Event        // target id -> buffered events
	workers map[string]chan message   // target id -> delivery queue
	status  map[string]Status         // target id -> last outcome
}

// New builds a Service; send is normally Shoutrrr.
func New(cfg func() config.Config, send SendFunc) *Service {
	return &Service{cfg: cfg, send: send, now: time.Now, in: make(chan Event, 512),
		pending: map[string][]Event{}, workers: map[string]chan message{}, status: map[string]Status{}}
}

// Send queues ev; it never blocks (a full queue drops the event).
func (s *Service) Send(ev Event) {
	if s == nil {
		return
	}
	if ev.Level == "" {
		ev.Level = Info
	}
	select {
	case s.in <- ev:
	default:
		log.Printf("notify: queue full, dropped %s event", ev.Key)
	}
}

// Run accepts queued events and flushes every Window until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(Window)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-s.in:
			s.accept(ev)
		case <-t.C:
			s.Flush()
		}
	}
}

// accept buffers ev for every enabled target that wants it.
func (s *Service) accept(ev Event) {
	cfg := s.cfg()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range cfg.Notifiers {
		if wants(t, ev) {
			s.pending[t.ID] = append(s.pending[t.ID], ev)
		}
	}
}

func wants(t config.Notifier, ev Event) bool {
	if !t.Enabled || t.URL == "" || levelRank(ev.Level) < levelRank(t.MinLevel) {
		return false
	}
	for _, k := range t.Events {
		if k == ev.Key {
			return true
		}
	}
	return false
}

// inQuiet reports whether now falls in t's quiet hours.
func inQuiet(t config.Notifier, now time.Time) bool {
	if t.QuietStart == t.QuietEnd {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	if t.QuietStart < t.QuietEnd {
		return m >= t.QuietStart && m < t.QuietEnd
	}
	return m >= t.QuietStart || m < t.QuietEnd // wraps midnight
}

// Flush drains everything already queued, then turns each target's
// buffer into messages for its worker. Info events inside quiet hours
// stay buffered.
func (s *Service) Flush() {
	for drained := false; !drained; {
		select {
		case ev := <-s.in:
			s.accept(ev)
		default:
			drained = true
		}
	}
	cfg := s.cfg()
	now := s.now()
	byID := map[string]config.Notifier{}
	for _, t := range cfg.Notifiers {
		byID[t.ID] = t
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, evs := range s.pending {
		t, ok := byID[id]
		if !ok || !t.Enabled {
			delete(s.pending, id)
			continue
		}
		quiet := inQuiet(t, now)
		var held, ready []Event
		for _, ev := range evs {
			if quiet && levelRank(ev.Level) == 0 {
				held = append(held, ev)
			} else {
				ready = append(ready, ev)
			}
		}
		if len(held) > 0 {
			s.pending[id] = held
		} else {
			delete(s.pending, id)
		}
		for _, m := range buildMessages(ready, cfg.PublicURL()) {
			s.enqueueLocked(id, m)
		}
	}
}

// buildMessages groups events by key (first-seen order) and coalesces
// any group larger than CoalesceAfter into one message.
func buildMessages(evs []Event, base string) []message {
	var order []string
	groups := map[string][]Event{}
	for _, ev := range evs {
		if _, ok := groups[ev.Key]; !ok {
			order = append(order, ev.Key)
		}
		groups[ev.Key] = append(groups[ev.Key], ev)
	}
	var out []message
	for _, k := range order {
		g := groups[k]
		if len(g) <= CoalesceAfter {
			for _, ev := range g {
				out = append(out, message{title: ev.Title, body: withLink(ev.Body, ev.Link, base)})
			}
			continue
		}
		title := fmt.Sprintf("%d × %s", len(g), g[0].Title)
		if g[0].Group != "" {
			title = fmt.Sprintf("%d %s", len(g), g[0].Group)
		}
		var lines []string
		for i, ev := range g {
			if i == 10 {
				lines = append(lines, fmt.Sprintf("… and %d more", len(g)-10))
				break
			}
			lines = append(lines, "• "+ev.Title)
		}
		out = append(out, message{title: title, body: strings.Join(lines, "\n")})
	}
	return out
}

func withLink(body, link, base string) string {
	if link == "" || base == "" {
		return body
	}
	if strings.HasPrefix(link, "#") {
		link = "/" + link
	}
	if body == "" {
		return base + link
	}
	return body + "\n" + base + link
}

// enqueueLocked hands m to target id's worker, starting it if needed.
func (s *Service) enqueueLocked(id string, m message) {
	ch, ok := s.workers[id]
	if !ok {
		ch = make(chan message, 64)
		s.workers[id] = ch
		go s.worker(id, ch)
	}
	select {
	case ch <- m:
	default:
		log.Printf("notify: target %s backlog full, dropped a message", id)
	}
}

// worker delivers one target's messages in order, with retries and
// exponential backoff. The URL is looked up per message so edits (or
// removal) apply to what's still queued.
func (s *Service) worker(id string, ch chan message) {
	for m := range ch {
		t, ok := s.target(id)
		if !ok || !t.Enabled {
			continue
		}
		var err error
		delay := RetryBackoff
		for i := 0; i < Tries; i++ {
			if err = s.send(t.URL, m.title, m.body); err == nil {
				break
			}
			if i < Tries-1 {
				time.Sleep(delay)
				delay *= 2
			}
		}
		s.record(id, err)
		if err != nil {
			log.Printf("notify: target %q: %v", t.Name, err)
		}
	}
}

func (s *Service) target(id string) (config.Notifier, bool) {
	for _, t := range s.cfg().Notifiers {
		if t.ID == id {
			return t, true
		}
	}
	return config.Notifier{}, false
}

func (s *Service) record(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[id]
	now := s.now().UTC().Format(time.RFC3339)
	if err == nil {
		st.LastSentAt, st.LastError, st.LastErrorAt = now, "", ""
	} else {
		st.LastError, st.LastErrorAt = err.Error(), now
	}
	s.status[id] = st
}

// Statuses returns every target's last delivery outcome.
func (s *Service) Statuses() map[string]Status {
	out := map[string]Status{}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.status {
		out[k] = v
	}
	return out
}

// Test sends "Test from Distillarr" to url right away (no queue, no
// retries) and records the outcome against id when it's a saved target.
func (s *Service) Test(id, url string) error {
	err := s.send(url, "Test from Distillarr", "If you can read this, notifications to this target work.")
	if id != "" {
		s.record(id, err)
	}
	return err
}
