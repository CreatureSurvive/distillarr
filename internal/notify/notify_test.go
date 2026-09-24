package notify

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
)

type sent struct{ url, title, body string }

type fakeSender struct {
	mu    sync.Mutex
	got   []sent
	fails int // fail this many calls first
}

func (f *fakeSender) send(url, title, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fails > 0 {
		f.fails--
		return errors.New("boom")
	}
	f.got = append(f.got, sent{url, title, body})
	return nil
}

func (f *fakeSender) all() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.got...)
}

func waitN(t *testing.T, f *fakeSender, n int) []sent {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := f.all(); len(got) >= n {
			time.Sleep(10 * time.Millisecond)
			return f.all()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("got %d messages, want %d", len(f.all()), n)
	return nil
}

func newSvc(targets []config.Notifier, base string) (*Service, *fakeSender) {
	RetryBackoff = time.Millisecond
	f := &fakeSender{}
	cfg := config.Config{Notifiers: targets, WebhookBaseURL: base}
	return New(func() config.Config { return cfg }, f.send), f
}

func target(id string, events ...string) config.Notifier {
	return config.Notifier{ID: id, Name: id, URL: "generic://" + id, Enabled: true, Events: events}
}

func TestRoutingAndLevels(t *testing.T) {
	warnOnly := target("b", "job_failed", "job_done")
	warnOnly.MinLevel = Warning
	disabled := target("c", "job_failed")
	disabled.Enabled = false
	s, f := newSvc([]config.Notifier{target("a", "job_done"), warnOnly, disabled}, "http://mt:8093/")

	s.Send(Event{Key: "job_done", Title: "Done", Body: "x", Link: "#/file/1"})
	s.Send(Event{Key: "job_failed", Level: Error, Title: "Failed"})
	s.Flush()

	got := waitN(t, f, 2)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, m := range got {
		switch m.url {
		case "generic://a":
			if m.title != "Done" || m.body != "x\nhttp://mt:8093/#/file/1" {
				t.Errorf("a got %+v", m)
			}
		case "generic://b":
			if m.title != "Failed" {
				t.Errorf("b got %+v", m)
			}
		default:
			t.Errorf("unexpected target %+v", m)
		}
	}
}

func TestCoalescing(t *testing.T) {
	s, f := newSvc([]config.Notifier{target("a", "job_done", "job_failed")}, "")
	for i := 0; i < 12; i++ {
		s.Send(Event{Key: "job_done", Title: "Job", Group: "jobs finished", Link: "#/x"})
	}
	s.Send(Event{Key: "job_failed", Level: Error, Title: "One failure"})
	s.Flush()
	got := waitN(t, f, 2)
	if len(got) != 2 || got[0].title != "12 jobs finished" || got[1].title != "One failure" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(got[0].body, "… and 2 more") {
		t.Errorf("summary body %q", got[0].body)
	}
	if strings.Contains(got[1].body, "#/") {
		t.Error("no public URL: relative links must be left out")
	}
}

func TestQuietHoursHoldInfoOnly(t *testing.T) {
	q := target("a", "job_done", "job_failed")
	q.QuietStart, q.QuietEnd = 22*60, 7*60 // wraps midnight
	s, f := newSvc([]config.Notifier{q}, "")
	now := time.Date(2026, 9, 24, 23, 30, 0, 0, time.Local)
	s.now = func() time.Time { return now }

	s.Send(Event{Key: "job_done", Title: "Info"})
	s.Send(Event{Key: "job_failed", Level: Error, Title: "Err"})
	s.Flush()
	got := waitN(t, f, 1)
	if len(got) != 1 || got[0].title != "Err" {
		t.Fatalf("during quiet hours got %+v", got)
	}

	now = time.Date(2026, 9, 25, 7, 0, 0, 0, time.Local)
	s.Flush()
	got = waitN(t, f, 2)
	if got[1].title != "Info" {
		t.Errorf("held info not delivered after quiet hours: %+v", got)
	}
}

func TestRetryAndStatus(t *testing.T) {
	s, f := newSvc([]config.Notifier{target("a", "k")}, "")
	f.fails = 2
	s.Send(Event{Key: "k", Title: "T"})
	s.Flush()
	waitN(t, f, 1)
	if st := s.Statuses()["a"]; st.LastError != "" || st.LastSentAt == "" {
		t.Errorf("status after success-on-3rd-try = %+v", st)
	}

	f.fails = 3
	s.Send(Event{Key: "k", Title: "T2"})
	s.Flush()
	deadline := time.Now().Add(time.Second)
	for s.Statuses()["a"].LastError == "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if st := s.Statuses()["a"]; st.LastError != "boom" {
		t.Errorf("status after 3 failures = %+v", st)
	}
}

func TestScrub(t *testing.T) {
	raw := "discord://SeCrEtToKeN123@9876543210?color=0x50D9ff"
	err := scrub(errors.New("failed to send to "+raw+" with token SeCrEtToKeN123 id 9876543210"), raw)
	if strings.Contains(err.Error(), "SeCrEtToKeN123") || strings.Contains(err.Error(), "9876543210") {
		t.Errorf("secret leaked: %v", err)
	}
}
