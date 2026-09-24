// SPDX-License-Identifier: GPL-3.0-or-later

package plex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestTest(t *testing.T) {
	var gotPath, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotToken = r.URL.Path, r.Header.Get("X-Plex-Token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"friendlyName":"example","version":"1.32.5.7349","machineIdentifier":"abc123"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-token")
	id, err := c.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/" {
		t.Errorf("wrong path: %s", gotPath)
	}
	if gotToken != "secret-token" {
		t.Errorf("token must travel in X-Plex-Token, got %q", gotToken)
	}
	if id.FriendlyName != "example" || id.Version != "1.32.5.7349" {
		t.Errorf("wrong identity: %+v", id)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusUnauthorized) })
	if _, err := c.Test(context.Background()); err == nil {
		t.Error("a non-2xx answer must be an error")
	}
	if _, err := (*Client)(nil).Test(context.Background()); err == nil {
		t.Error("an unconfigured client must fail, not panic")
	}
}

func TestSections(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Directory":[
			{"key":"1","type":"movie","title":"Movies","Location":[{"id":1,"path":"/data/movies"}]},
			{"key":"2","type":"show","title":"TV Shows","Location":[{"id":2,"path":"/data/tvshows"}]}
		]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	secs, err := c.Sections(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 2 || secs[0].Key != "1" || secs[0].Location[0].Path != "/data/movies" || secs[1].Type != "show" {
		t.Errorf("wrong sections: %+v", secs)
	}
}

func TestWalkSectionPaginates(t *testing.T) {
	var gotTypes []string
	pages := [][]byte{
		[]byte(`{"MediaContainer":{"totalSize":3,"Metadata":[{"ratingKey":"1"},{"ratingKey":"2"}]}}`),
		[]byte(`{"MediaContainer":{"totalSize":3,"Metadata":[{"ratingKey":"3"}]}}`),
	}
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/sections/2/all" {
			http.NotFound(w, r)
			return
		}
		gotTypes = append(gotTypes, r.URL.Query().Get("type"))
		w.Header().Set("Content-Type", "application/json")
		w.Write(pages[call])
		call++
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	var got []string
	err := c.WalkSection(context.Background(), "2", 4, func(items []Item) error {
		for _, it := range items {
			got = append(got, it.RatingKey)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != "1" || got[2] != "3" {
		t.Errorf("wrong items: %v", got)
	}
	if call != 2 {
		t.Errorf("expected 2 pages, got %d", call)
	}
	for _, ty := range gotTypes {
		if ty != "4" {
			t.Errorf("type=4 must be sent on every page, got %q", ty)
		}
	}
}

func TestWalkSectionOmitsTypeWhenZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "" {
			t.Errorf("type must be omitted, got %q", r.URL.Query().Get("type"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"totalSize":0,"Metadata":[]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.WalkSection(context.Background(), "1", 0, func([]Item) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRefresh(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("path")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.Refresh(context.Background(), "3", "/data/movies/Some Movie"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/library/sections/3/refresh" {
		t.Errorf("wrong path: %s", gotPath)
	}
	if gotQuery != "/data/movies/Some Movie" {
		t.Errorf("wrong path query: %s", gotQuery)
	}
}

func TestMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/metadata/42" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[{"ratingKey":"42","addedAt":1700000000}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	it, err := c.Metadata(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if it.RatingKey != "42" || it.AddedAt != 1700000000 {
		t.Errorf("wrong item: %+v", it)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[]}}`))
	})
	if _, err := c.Metadata(context.Background(), "42"); err == nil {
		t.Error("an empty Metadata array must be an error, not a nil dereference")
	}
}

func TestSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status/sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[
			{"ratingKey":"100","Media":[{"Part":[{"file":"/data/movies/Movie/Movie.mkv"}]}],
			 "TranscodeSession":{"videoDecision":"transcode"}},
			{"ratingKey":"200","Media":[{"Part":[{"file":"/data/movies/Direct/Direct.mp4"}]}]},
			{"ratingKey":"300","Media":[{"Part":[{"file":"/data/movies/AudioOnly/A.mkv"}]}],
			 "TranscodeSession":{"videoDecision":"copy"}}
		]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	sessions, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}
	if sessions[0].TranscodeSession == nil || sessions[0].TranscodeSession.VideoDecision != "transcode" {
		t.Errorf("expected a video transcode, got %+v", sessions[0].TranscodeSession)
	}
	if sessions[1].TranscodeSession != nil {
		t.Errorf("direct play must have no TranscodeSession, got %+v", sessions[1].TranscodeSession)
	}
	if sessions[2].Media[0].Part[0].File != "/data/movies/AudioOnly/A.mkv" {
		t.Errorf("wrong file: %+v", sessions[2])
	}
}

func TestSetAddedAt(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.SetAddedAt(context.Background(), "1", "42", 1, 1700000000, true); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/library/sections/1/all" {
		t.Errorf("wrong path: %s", gotPath)
	}
	if gotQuery.Get("id") != "42" || gotQuery.Get("type") != "1" ||
		gotQuery.Get("addedAt.value") != "1700000000" || gotQuery.Get("addedAt.locked") != "1" {
		t.Errorf("wrong query: %v", gotQuery)
	}
}
