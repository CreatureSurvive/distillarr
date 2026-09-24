// SPDX-License-Identifier: GPL-3.0-or-later

// Package jellystat reads playback history from an optional Jellystat
// server, so forced-transcode detection and play counts don't
// have to wait for new sessions.
package jellystat

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

// Client talks to one Jellystat server with an API key.
type Client struct {
	Base string
	Key  string
	HC   *http.Client
}

func New(base, key string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Key: key, HC: &http.Client{Timeout: 60 * time.Second}}
}

// Activity is one playback session as Jellystat stored it.
type Activity struct {
	NowPlayingItemID string    `json:"NowPlayingItemId"`
	EpisodeID        string    `json:"EpisodeId"`
	PlayMethod       string    `json:"PlayMethod"` // DirectPlay | DirectStream | Transcode
	At               time.Time `json:"ActivityDateInserted"`
	TranscodingInfo  *struct {
		TranscodeReasons []string `json:"TranscodeReasons"`
	} `json:"TranscodingInfo"`
}

// ItemID is the Jellyfin id of the file that played: the episode for a
// series, else the item itself.
func (a Activity) ItemID() string {
	if a.EpisodeID != "" && a.EpisodeID != "1" {
		return a.EpisodeID
	}
	return a.NowPlayingItemID
}

// Direct reports a session with no transcoding at all.
func (a Activity) Direct() bool { return a.PlayMethod == "DirectPlay" }

// Reasons are Jellyfin's raw transcode reasons, if any.
func (a Activity) Reasons() []string {
	if a.TranscodingInfo == nil {
		return nil
	}
	return a.TranscodingInfo.TranscodeReasons
}

type historyPage struct {
	Pages   int `json:"pages"`
	Results []struct {
		Results []Activity `json:"results"`
	} `json:"results"`
}

func (c *Client) page(ctx context.Context, page, size int, since time.Time) (*historyPage, error) {
	q := url.Values{"page": {strconv.Itoa(page)}, "size": {strconv.Itoa(size)}, "sort": {"ActivityDateInserted"}, "desc": {"true"}}
	if !since.IsZero() {
		f, _ := json.Marshal([]map[string]string{{"field": "ActivityDateInserted", "min": since.UTC().Format(time.RFC3339)}})
		q.Set("filters", string(f))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/api/getHistory?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-token", c.Key)
	resp, err := c.HC.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("GET /api/getHistory: %s: %s", resp.Status, b)
	}
	var out historyPage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Test checks the URL and key, returning how many history pages exist.
func (c *Client) Test(ctx context.Context) (int, error) {
	p, err := c.page(ctx, 1, 1, time.Time{})
	if err != nil {
		return 0, err
	}
	return p.Pages, nil
}

// History walks every session whose item group was active at or after
// since (zero: everything), newest first, calling fn per page.
func (c *Client) History(ctx context.Context, since time.Time, fn func([]Activity) error) error {
	for page := 1; ; page++ {
		p, err := c.page(ctx, page, 200, since)
		if err != nil {
			return err
		}
		var acts []Activity
		for _, g := range p.Results {
			acts = append(acts, g.Results...)
		}
		if err := fn(acts); err != nil {
			return err
		}
		if page >= p.Pages || len(p.Results) == 0 {
			return nil
		}
	}
}
