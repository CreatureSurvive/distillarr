package pathmap

import "testing"

func TestSingleRuleBothWays(t *testing.T) {
	m, err := Parse("/data=/srv/media")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ remote, local string }{
		{"/data/movies/A (2001)/A.mp4", "/srv/media/movies/A (2001)/A.mp4"},
		{"/data", "/srv/media"},
	} {
		if got := m.ToLocal(tc.remote); got != tc.local {
			t.Errorf("ToLocal(%q) = %q, want %q", tc.remote, got, tc.local)
		}
		if got := m.ToRemote(tc.local); got != tc.remote {
			t.Errorf("ToRemote(%q) = %q, want %q", tc.local, got, tc.remote)
		}
	}
	for _, p := range []string{"/mnt/other/x.mp4", "/srv/media2/x.mp4"} {
		if got := m.ToRemote(p); got != p {
			t.Errorf("%q is not under the mapped root, got %q", p, got)
		}
	}
}

func TestEmptyMapNoRewrite(t *testing.T) {
	var m Map
	if !m.Empty() {
		t.Fatal("zero Map should be Empty")
	}
	if got := m.ToRemote("/srv/media/x.mp4"); got != "/srv/media/x.mp4" {
		t.Errorf("no mapping configured means no rewrite, got %q", got)
	}
	if got := m.ToLocal("/data/x.mp4"); got != "/data/x.mp4" {
		t.Errorf("no mapping configured means no rewrite, got %q", got)
	}
}

func TestMultipleRulesSeparators(t *testing.T) {
	for _, s := range []string{
		"/data=/srv/media;/data4k=/srv/media4k",
		"/data=/srv/media\n/data4k=/srv/media4k",
	} {
		m, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.ToLocal("/data/x.mp4"); got != "/srv/media/x.mp4" {
			t.Errorf("%q: ToLocal(/data/x.mp4) = %q", s, got)
		}
		if got := m.ToLocal("/data4k/x.mp4"); got != "/srv/media4k/x.mp4" {
			t.Errorf("%q: ToLocal(/data4k/x.mp4) = %q", s, got)
		}
	}
}

// Overlapping remote roots: the longest (most specific) prefix must win,
// regardless of the order the rules were written in.
func TestLongestPrefixWins(t *testing.T) {
	for _, s := range []string{
		"/data=/srv/media;/data/4k=/srv/media4k",
		"/data/4k=/srv/media4k;/data=/srv/media",
	} {
		m, err := Parse(s)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.ToLocal("/data/4k/movie.mp4"); got != "/srv/media4k/movie.mp4" {
			t.Errorf("%q: ToLocal(/data/4k/movie.mp4) = %q, want the more specific rule", s, got)
		}
		if got := m.ToLocal("/data/movies/x.mp4"); got != "/srv/media/movies/x.mp4" {
			t.Errorf("%q: ToLocal(/data/movies/x.mp4) = %q", s, got)
		}
	}
}

func TestTrailingSlashesIgnored(t *testing.T) {
	m, err := Parse("/data/=/srv/media/")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ToLocal("/data/x.mp4"); got != "/srv/media/x.mp4" {
		t.Errorf("got %q", got)
	}
	if got := m.ToLocal("/data"); got != "/srv/media" {
		t.Errorf("got %q", got)
	}
}

func TestSegmentBoundary(t *testing.T) {
	m, _ := Parse("/data=/srv/media")
	// "/database" merely shares a prefix with "/data" and must not match.
	if got := m.ToLocal("/database/x"); got != "/database/x" {
		t.Errorf("got %q, want unchanged (segment boundary)", got)
	}
}

func TestRoundTrip(t *testing.T) {
	m, _ := Parse("/data=/srv/media")
	for _, p := range []string{"/data/movies/x.mp4", "/data"} {
		if got := m.ToLocal(m.ToRemote(m.ToLocal(p))); got != m.ToLocal(p) {
			t.Errorf("round trip broke for %q: got %q", p, got)
		}
	}
	local := "/srv/media/movies/x.mp4"
	if got := m.ToRemote(local); m.ToLocal(got) != local {
		t.Errorf("ToLocal(ToRemote(%q)) = %q, want %q", local, m.ToLocal(got), local)
	}
}

func TestBlankAndMalformedEntriesIgnored(t *testing.T) {
	m, err := Parse("  ; /data=/srv/media ; not-a-rule ;")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ToLocal("/data/x"); got != "/srv/media/x" {
		t.Errorf("got %q", got)
	}
}
