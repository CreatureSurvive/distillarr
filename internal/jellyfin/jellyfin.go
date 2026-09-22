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

// auth sends the key in the Authorization header. Jellyfin 10.11+
// rejects the legacy ?api_key= query parameter by default.
func (c *Client) auth(req *http.Request) {
	req.Header.Set("Authorization", fmt.Sprintf(`MediaBrowser Client="mediatrans", Device="mediatrans", DeviceId="mediatrans", Version="1", Token="%s"`, c.Key))
}

func (c *Client) get(ctx context.Context, out any, path string, q url.Values) error {
	if c == nil || c.Base == "" || c.Key == "" {
		return fmt.Errorf("jellyfin not configured")
	}
	u := c.Base + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	c.auth(req)
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
	c.auth(req)
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

// Folder is one Jellyfin library with its on-disk locations.
type Folder struct {
	Name           string   `json:"Name"`
	CollectionType string   `json:"CollectionType"`
	Locations      []string `json:"Locations"`
}

// Libraries lists the server's libraries (used to verify path mapping).
func (c *Client) Libraries(ctx context.Context) ([]Folder, error) {
	var f []Folder
	err := c.get(ctx, &f, "/Library/VirtualFolders", url.Values{})
	return f, err
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
	Genres           []string          `json:"Genres"`
}

type itemsResp struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

// WalkItems pages through every Movie/Episode with its Path.
func (c *Client) WalkItems(ctx context.Context, fn func([]Item) error) error {
	return c.walk(ctx, "Movie,Episode", fn)
}

// WalkSeries pages through every Series (genres + overview live there).
func (c *Client) WalkSeries(ctx context.Context, fn func([]Item) error) error {
	return c.walk(ctx, "Series", fn)
}

func (c *Client) walk(ctx context.Context, types string, fn func([]Item) error) error {
	const page = 1000
	for start := 0; ; start += page {
		var r itemsResp
		q := url.Values{
			"Recursive":        {"true"},
			"IncludeItemTypes": {types},
			"Fields":           {"Path,Overview,ParentId,Genres"},
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
func (c *Client) ImageURL(itemID, kind string, maxW int) string {
	if kind == "" {
		kind = "Primary"
	}
	return fmt.Sprintf("%s/Items/%s/Images/%s?maxWidth=%d&quality=85", c.Base, itemID, kind, maxW)
}

// FetchImage downloads an item image (kind: Primary | Backdrop | Thumb).
func (c *Client) FetchImage(ctx context.Context, itemID, kind string, maxW int) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ImageURL(itemID, kind, maxW), nil)
	if err != nil {
		return nil, "", err
	}
	c.auth(req)
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

// MediaUpdated tells Jellyfin that a file appeared, changed or vanished, so it
// scans just that path instead of waiting for the next library scan (the call
// Sonarr and Radarr make). updateType is Created, Modified or Deleted. path
// must be Jellyfin's view of the file.
func (c *Client) MediaUpdated(ctx context.Context, path, updateType string) error {
	body := map[string]any{"Updates": []map[string]string{{"Path": path, "UpdateType": updateType}}}
	return c.post(ctx, "/Library/Media/Updated", url.Values{}, body)
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
	return c.post(ctx, "/Items/"+itemID, url.Values{}, item)
}
