// Package bazarr is the optional Bazarr integration: Bazarr stays
// the subtitle source, and Distillarr only tells it when a file changed
// (so it rescans the disk) or asks it to search for missing subtitles.
// Bazarr identifies items by their Sonarr series id / Radarr movie id,
// which arr_items already records.
package bazarr

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

// Actions accepted by Bazarr's PATCH /api/series and /api/movies.
const (
	ScanDisk      = "scan-disk"
	SearchMissing = "search-missing"
)

// Client talks to one Bazarr instance with an API key.
type Client struct {
	Base string
	Key  string
	HC   *http.Client
}

func New(base, key string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Key: key,
		HC: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, out any) error {
	if c == nil || c.Base == "" || c.Key == "" {
		return fmt.Errorf("bazarr not configured")
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-KEY", c.Key)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, b)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Status is the subset of /api/system/status the test endpoint shows.
type Status struct {
	Version       string `json:"bazarr_version"`
	SonarrVersion string `json:"sonarr_version"`
	RadarrVersion string `json:"radarr_version"`
}

func (c *Client) Status(ctx context.Context) (*Status, error) {
	var out struct {
		Data Status `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/system/status", nil, &out); err != nil {
		return nil, err
	}
	return &out.Data, nil
}

// Item is one series or movie as Bazarr lists it; only the path matters
// here (for the path-mapping check).
type Item struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// SamplePaths returns the first series and first movie Bazarr knows
// about (either may be missing when that side isn't connected).
func (c *Client) SamplePaths(ctx context.Context) ([]Item, error) {
	var out []Item
	for _, p := range []string{"/api/series", "/api/movies"} {
		var r struct {
			Data []Item `json:"data"`
		}
		if err := c.do(ctx, http.MethodGet, p, url.Values{"start": {"0"}, "length": {"1"}}, &r); err != nil {
			return out, err
		}
		out = append(out, r.Data...)
	}
	return out, nil
}

// Series runs action on one Sonarr series (Bazarr's seriesid is Sonarr's id).
func (c *Client) Series(ctx context.Context, seriesID int64, action string) error {
	return c.do(ctx, http.MethodPatch, "/api/series",
		url.Values{"seriesid": {strconv.FormatInt(seriesID, 10)}, "action": {action}}, nil)
}

// Movie runs action on one Radarr movie (Bazarr's radarrid is Radarr's id).
func (c *Client) Movie(ctx context.Context, movieID int64, action string) error {
	return c.do(ctx, http.MethodPatch, "/api/movies",
		url.Values{"radarrid": {strconv.FormatInt(movieID, 10)}, "action": {action}}, nil)
}
