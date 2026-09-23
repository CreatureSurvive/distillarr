// Package plex is the optional Plex Media Server integration: library
// sync (mapping Plex items onto local file paths) and post-replace
// section refresh, mirroring internal/jellyfin's shape.
package plex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to one Plex Media Server with a token.
type Client struct {
	Base  string // e.g. http://host.docker.internal:32400
	Token string
	HC    *http.Client
}

func New(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token,
		HC: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) get(ctx context.Context, out any, path string, q url.Values) error {
	if c == nil || c.Base == "" || c.Token == "" {
		return fmt.Errorf("plex not configured")
	}
	if q == nil {
		q = url.Values{}
	}
	u := c.Base + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Plex-Token", c.Token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, b)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Identity is the server's root MediaContainer (connection test): unlike
// the unauthenticated /identity endpoint, GET / requires the token and
// carries the human-readable server name, so it doubles as the auth
// check (matching jellyfin.Client.Test's /System/Info).
type Identity struct {
	FriendlyName      string `json:"friendlyName"`
	Version           string `json:"version"`
	MachineIdentifier string `json:"machineIdentifier"`
}

type identityResp struct {
	MediaContainer Identity `json:"MediaContainer"`
}

// Test verifies connectivity and the token.
func (c *Client) Test(ctx context.Context) (*Identity, error) {
	var r identityResp
	if err := c.get(ctx, &r, "/", nil); err != nil {
		return nil, err
	}
	return &r.MediaContainer, nil
}

// Location is one on-disk root of a library Section.
type Location struct {
	ID   int    `json:"id"`
	Path string `json:"path"`
}

// Section is one Plex library (movie or show, among other types).
type Section struct {
	Key      string     `json:"key"`
	Type     string     `json:"type"` // movie | show | artist | photo
	Title    string     `json:"title"`
	Location []Location `json:"Location"`
}

type sectionsResp struct {
	MediaContainer struct {
		Directory []Section `json:"Directory"`
	} `json:"MediaContainer"`
}

// Sections lists the server's libraries (used to verify path mapping).
func (c *Client) Sections(ctx context.Context) ([]Section, error) {
	var r sectionsResp
	if err := c.get(ctx, &r, "/library/sections", nil); err != nil {
		return nil, err
	}
	return r.MediaContainer.Directory, nil
}

// Part is one file backing a Media version of an Item.
type Part struct {
	File string `json:"file"`
}

// Media is one version (e.g. resolution/cut) of an Item; most items have
// exactly one, but an item can have several, each with several Parts.
type Media struct {
	Part []Part `json:"Part"`
}

// Item is the subset of a Plex library item we cache: a movie, or (with
// section-level type=4) an episode flattened out of its show/season.
type Item struct {
	RatingKey string  `json:"ratingKey"`
	Title     string  `json:"title"`
	Type      string  `json:"type"`
	AddedAt   int64   `json:"addedAt"` // unix seconds
	Media     []Media `json:"Media"`
}

type itemsResp struct {
	MediaContainer struct {
		TotalSize int    `json:"totalSize"`
		Offset    int    `json:"offset"`
		Metadata  []Item `json:"Metadata"`
	} `json:"MediaContainer"`
}

// WalkSection pages through every item in a library section. typ is the
// Plex metadata type to request (1 = movie, 4 = episode — the latter
// flattens every episode out of a show section in one listing rather
// than requiring a per-series walk); 0 omits the filter, which is Plex's
// own default for a movie section.
func (c *Client) WalkSection(ctx context.Context, sectionKey string, typ int, fn func([]Item) error) error {
	const page = 200
	for start := 0; ; start += page {
		var r itemsResp
		q := url.Values{
			"X-Plex-Container-Start": {strconv.Itoa(start)},
			"X-Plex-Container-Size":  {strconv.Itoa(page)},
		}
		if typ > 0 {
			q.Set("type", strconv.Itoa(typ))
		}
		if err := c.get(ctx, &r, "/library/sections/"+sectionKey+"/all", q); err != nil {
			return err
		}
		if len(r.MediaContainer.Metadata) == 0 {
			return nil
		}
		if err := fn(r.MediaContainer.Metadata); err != nil {
			return err
		}
		if start+len(r.MediaContainer.Metadata) >= r.MediaContainer.TotalSize {
			return nil
		}
	}
}

// Refresh triggers a partial library scan of one directory (used after a
// replace so Plex re-reads the new file without a full section scan).
// path must be Plex's view of the directory.
func (c *Client) Refresh(ctx context.Context, sectionKey, path string) error {
	return c.get(ctx, nil, "/library/sections/"+sectionKey+"/refresh", url.Values{"path": {path}})
}
