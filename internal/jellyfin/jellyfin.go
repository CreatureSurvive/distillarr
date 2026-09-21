// Package jellyfin is the optional integration client: library
// enrichment (posters/overview), post-replace refresh, and the
// DateCreated patch that preserves "date added" across in-place
// replaces (Linux cannot rewrite crtime).
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one Jellyfin server with an API key.
type Client struct {
	Base string // e.g. http://host.docker.internal:8096
	Key  string
	HC   *http.Client
}

func New(base, key string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Key: key,
		HC: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) get(ctx context.Context, out any, path string, q url.Values) error {
	if c == nil || c.Base == "" || c.Key == "" {
		return fmt.Errorf("jellyfin not configured")
	}
	q.Set("api_key", c.Key)
	u := c.Base + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("GET %s: %s: %s", path, resp.Status, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) post(ctx context.Context, path string, q url.Values, body any) error {
	if c == nil || c.Base == "" || c.Key == "" {
		return fmt.Errorf("jellyfin not configured")
	}
	q.Set("api_key", c.Key)
	u := c.Base + path + "?" + q.Encode()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("POST %s: %s: %s", path, resp.Status, b)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// SystemInfo is the /System/Info payload (connection test).
type SystemInfo struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
}

// Test verifies connectivity and the API key.
func (c *Client) Test(ctx context.Context) (*SystemInfo, error) {
	var si SystemInfo
	if err := c.get(ctx, &si, "/System/Info", url.Values{}); err != nil {
		return nil, err
	}
	return &si, nil
}

// Item is the subset of a Jellyfin item we cache.
type Item struct {
	ID               string            `json:"Id"`
	Name             string            `json:"Name"`
	Path             string            `json:"Path"`
	Type             string            `json:"Type"`
	SeriesID         string            `json:"SeriesId"`
	SeasonID         string            `json:"SeasonId"`
	ParentID         string            `json:"ParentId"`
	Overview         string            `json:"Overview"`
	ProductionYear   int               `json:"ProductionYear"`
	ParentIndexNumber int              `json:"ParentIndexNumber"` // season
	IndexNumber      int               `json:"IndexNumber"`       // episode
	SeriesName       string            `json:"SeriesName"`
	ImageTags        map[string]string `json:"ImageTags"`
}

type itemsResp struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

// WalkItems pages through every Movie/Episode with its Path.
func (c *Client) WalkItems(ctx context.Context, fn func([]Item) error) error {
	const page = 1000
	for start := 0; ; start += page {
		var r itemsResp
		q := url.Values{
			"Recursive":        {"true"},
			"IncludeItemTypes": {"Movie,Episode"},
			"Fields":           {"Path,Overview,ParentId"},
			"SortBy":           {"Id"},
			"SortOrder":        {"Ascending"},
			"StartIndex":       {fmt.Sprint(start)},
			"Limit":            {fmt.Sprint(page)},
		}
		if err := c.get(ctx, &r, "/Items", q); err != nil {
			return err
		}
		if len(r.Items) == 0 {
			return nil
		}
		if err := fn(r.Items); err != nil {
			return err
		}
		if start+len(r.Items) >= r.TotalRecordCount {
			return nil
		}
	}
}

// ImageURL builds an image URL for server-side proxying.
func (c *Client) ImageURL(itemID string) string {
	return c.Base + "/Items/" + itemID + "/Images/Primary?api_key=" + url.QueryEscape(c.Key)
}

// FetchImage downloads an item image.
func (c *Client) FetchImage(ctx context.Context, itemID string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ImageURL(itemID), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, "", fmt.Errorf("image: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return b, resp.Header.Get("Content-Type"), err
}

// Refresh triggers a metadata refresh for an item (used after a
// replace so Jellyfin re-reads the new file).
func (c *Client) Refresh(ctx context.Context, itemID string) error {
	q := url.Values{
		"Recursive":            {"false"},
		"MetadataRefreshMode":  {"FullRefresh"},
		"ImageRefreshMode":     {"Default"},
		"ReplaceAllMetadata":   {"false"},
	}
	return c.post(ctx, "/Items/"+itemID+"/Refresh", q, nil)
}

// PatchDateCreated restores an item's DateCreated to the original
// file's birth time: GET the item, set DateCreated, POST it back.
func (c *Client) PatchDateCreated(ctx context.Context, itemID string, btimeUTC time.Time) error {
	var uid struct {
		ID string `json:"Id"`
	}
	// find an admin user id
	var users []struct {
		ID string `json:"Id"`
	}
	if err := c.get(ctx, &users, "/Users", url.Values{}); err != nil {
		return err
	}
	if len(users) == 0 {
		return fmt.Errorf("no jellyfin users")
	}
	uid.ID = users[0].ID

	var item map[string]any
	if err := c.get(ctx, &item, "/Users/"+uid.ID+"/Items/"+itemID, url.Values{}); err != nil {
		return err
	}
	item["DateCreated"] = btimeUTC.UTC().Format("2006-01-02T15:04:05.0000000Z")
	return c.post(ctx, "/Users/"+uid.ID+"/Items/"+itemID, url.Values{}, item)
}
