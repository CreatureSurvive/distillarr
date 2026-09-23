// Package arr is the client for Sonarr and Radarr's near-identical v3
// APIs: one Client type serves both, since almost every endpoint this
// app needs (status, root folders, custom formats, quality profiles,
// naming, tags) has the same shape on both apps. Endpoints that differ
// by kind (series vs movie, episodefile vs moviefile) branch on Kind.
package arr

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

// Kind distinguishes which app an instance is.
type Kind string

const (
	Sonarr Kind = "sonarr"
	Radarr Kind = "radarr"
)

// Client talks to one Sonarr or Radarr instance with an API key.
type Client struct {
	Base string // e.g. http://localhost:8989, no trailing slash
	Key  string
	Kind Kind
	HC   *http.Client
}

// New builds a client with a 15s default timeout, matching the plan's
// spec for read requests; long-running commands should pass their own
// context deadline through the *Ctx methods instead of relying on this.
func New(base, key string, kind Kind) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Key: key, Kind: kind,
		HC: &http.Client{Timeout: 15 * time.Second}}
}

func (c *Client) auth(req *http.Request) {
	req.Header.Set("X-Api-Key", c.Key)
}

// FriendlyError turns a raw client error into something worth showing a
// user in Settings, the same way the Jellyfin client's test does.
func FriendlyError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"):
		return "The API key was rejected (401). Check Settings → General → Security in Sonarr/Radarr."
	case strings.Contains(msg, "connection refused"):
		return "Nothing is listening at that URL."
	case strings.Contains(msg, "no such host"):
		return "That hostname doesn't resolve from inside the container."
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "Timed out reaching the server. Check the URL and that the container can reach it."
	}
	return msg
}

func (c *Client) get(ctx context.Context, out any, path string, q url.Values) error {
	if c == nil || c.Base == "" || c.Key == "" {
		return fmt.Errorf("not configured")
	}
	u := c.Base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
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
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// post sends a JSON body (nil for none) and decodes a JSON response into
// out (nil to discard it), matching what the tag-create and command
// endpoints return.
func (c *Client) post(ctx context.Context, out any, path string, body any) error {
	return c.write(ctx, http.MethodPost, out, path, body)
}

func (c *Client) put(ctx context.Context, out any, path string, body any) error {
	return c.write(ctx, http.MethodPut, out, path, body)
}

func (c *Client) write(ctx context.Context, method string, out any, path string, body any) error {
	if c == nil || c.Base == "" || c.Key == "" {
		return fmt.Errorf("not configured")
	}
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, &buf)
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
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, b)
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Status is the /api/v3/system/status response (connection test).
type Status struct {
	Version string `json:"version"`
	AppName string `json:"appName"`
}

// Test verifies connectivity and the API key.
func (c *Client) Test(ctx context.Context) (*Status, error) {
	var st Status
	if err := c.get(ctx, &st, "/api/v3/system/status", nil); err != nil {
		return nil, err
	}
	return &st, nil
}

// RootFolder is one configured library root.
type RootFolder struct {
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	Accessible bool   `json:"accessible"`
	FreeSpace  int64  `json:"freeSpace"`
}

// RootFolders lists the instance's configured library roots, used to
// verify its path map against what this container can see.
func (c *Client) RootFolders(ctx context.Context) ([]RootFolder, error) {
	var rf []RootFolder
	err := c.get(ctx, &rf, "/api/v3/rootfolder", nil)
	return rf, err
}
