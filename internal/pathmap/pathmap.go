// Package pathmap rewrites paths between how an external service sees the
// library (its own mount point) and how this container sees it. Every
// integration that connects to something outside the container (Jellyfin,
// Plex, Sonarr, Radarr, Bazarr, …) needs its own instance of this, because
// each one may mount the media root at a different path.
package pathmap

import "strings"

// rule is one "remote=local" pair, both sides right-trimmed of slashes.
type rule struct {
	remote, local string
}

// Map is a parsed, ready-to-use set of path rules.
type Map struct {
	rules []rule // longest remote prefix first
}

// Parse reads rules separated by ';' or newlines, each "remote=local"
// (matching the existing single-rule "from=to" convention: from is the
// remote/external path, to is the local one). Blank entries are skipped.
// Rules are sorted so the longest remote prefix is tried first, which is
// what makes overlapping roots (e.g. "/data" and "/data/4k") resolve to
// the more specific one.
func Parse(s string) (Map, error) {
	var m Map
	for _, part := range splitRules(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		remote, local, ok := strings.Cut(part, "=")
		if !ok {
			continue // not a rule; ignore rather than fail, same as the old single-rule code
		}
		remote = strings.TrimRight(strings.TrimSpace(remote), "/")
		local = strings.TrimRight(strings.TrimSpace(local), "/")
		if remote == "" {
			continue
		}
		m.rules = append(m.rules, rule{remote: remote, local: local})
	}
	// Longest remote prefix wins when two rules could both match.
	for i := 1; i < len(m.rules); i++ {
		for j := i; j > 0 && len(m.rules[j].remote) > len(m.rules[j-1].remote); j-- {
			m.rules[j], m.rules[j-1] = m.rules[j-1], m.rules[j]
		}
	}
	return m, nil
}

func splitRules(s string) []string {
	s = strings.ReplaceAll(s, "\n", ";")
	return strings.Split(s, ";")
}

// segmentMatch reports whether p is exactly prefix, or prefix followed by a
// '/', so "/data" matches "/data/x" but not "/database".
func segmentMatch(p, prefix string) bool {
	return p == prefix || strings.HasPrefix(p, prefix+"/")
}

// ToLocal rewrites a remote-side path to this container's view. A path
// that matches no rule is returned unchanged.
func (m Map) ToLocal(p string) string {
	for _, r := range m.rules {
		if segmentMatch(p, r.remote) {
			return r.local + strings.TrimPrefix(p, r.remote)
		}
	}
	return p
}

// ToRemote is ToLocal's inverse: this container's path as the remote
// service would see it. A path that matches no rule is returned unchanged.
func (m Map) ToRemote(p string) string {
	for _, r := range m.rules {
		if r.local != "" && segmentMatch(p, r.local) {
			return r.remote + strings.TrimPrefix(p, r.local)
		}
	}
	return p
}

// Empty reports whether no usable rule was parsed.
func (m Map) Empty() bool { return len(m.rules) == 0 }
