// SPDX-License-Identifier: GPL-3.0-or-later

package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gureetk/runnel/internal/meta/jellyfin/standin"
)

func standIn(t *testing.T) (*standin.Server, string) {
	t.Helper()
	s := &standin.Server{Key: "0123456789abcdef0123456789abcdef", Version: "12.4.0",
		Users: []standin.User{{ID: "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c", Name: "Ada"}, {ID: "5b4d20b7e5df4f2a8a2d3e4f5a6b7c8d", Name: "noor"}}}
	for i := range 1203 {
		s.Items = append(s.Items, standin.Item{ID: fmt.Sprintf("%032x", i+1), Name: fmt.Sprintf("Film %04d", i), Type: "Movie",
			Path: fmt.Sprintf("/media/films/Film %04d (2001)/Film %04d (2001).mkv", i, i), ProviderIDs: map[string]string{"Tmdb": fmt.Sprint(1000 + i)},
			UserData: map[string]standin.UserData{"4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c": {Played: i%2 == 0, PlayCount: i % 3,
				LastPlayedDate: "2026-11-01T20:15:30.1234567Z", PositionTicks: 123_450_000}}})
	}
	s.Items = append(s.Items, standin.Item{ID: "f0", Name: "A virtual episode", Type: "Episode"})
	ts := s.Start(t)
	return s, ts.URL
}

func TestNew(t *testing.T) {
	for _, u := range []string{"", "jellyfin.lan:8096", "ftp://jellyfin.lan", "http://", "http://jf.lan/?x=1", "http://ada:pw@jf.lan"} {
		if _, err := New(u, "key", nil); err == nil {
			t.Errorf("%q: no error", u)
		}
	}
	for _, k := range []string{"", "a\"b", "a,b", "a b", "é", string(make([]byte, 201))} {
		if _, err := New("http://jf.lan", k, nil); err == nil {
			t.Errorf("key %q: no error", k)
		}
	}
	c, err := New(" https://jf.lan/jellyfin/ ", "key", nil)
	if err != nil || c.base != "https://jf.lan/jellyfin" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestVersionAndUsers(t *testing.T) {
	s, url := standIn(t)
	ctx := context.Background()
	c, _ := New(url, s.Key, nil)
	if v, err := c.Version(ctx); err != nil || v != "12.4.0" {
		t.Fatalf("%q %v", v, err)
	}
	if r := s.Requests()[0]; r.Header.Get("Authorization") != "" {
		t.Fatalf("the public call carried the key: %q", r.Header.Get("Authorization"))
	}
	users, err := c.Users(ctx)
	if err != nil || len(users) != 2 || users[0].Name != "Ada" || users[0].ID != "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c" {
		t.Fatalf("%+v %v", users, err)
	}
	if got := s.Requests()[1].Header.Get("Authorization"); got != `MediaBrowser Token="`+s.Key+`"` {
		t.Fatalf("the header: %q", got)
	}
	wrong, _ := New(url, "another-key", nil)
	if _, err := wrong.Users(ctx); !errors.Is(err, ErrBadKey) {
		t.Fatalf("a wrong key: %v", err)
	}
}

// TestTheStandInRefusesTheOlderWays: as Jellyfin 12 does by default, the
// stand-in reads the key only from the Authorization header and the ApiKey
// parameter.
func TestTheStandInRefusesTheOlderWays(t *testing.T) {
	s, url := standIn(t)
	for _, c := range []struct {
		header, value, query string
		want                 int
	}{
		{"Authorization", `MediaBrowser Token="` + s.Key + `"`, "", 200},
		{"Authorization", `MediaBrowser Client="Runnel", Device="server", DeviceId="x", Version="0", Token="` + s.Key + `"`, "", 200},
		{"", "", "?ApiKey=" + s.Key, 200},
		{"X-Emby-Token", s.Key, "", 401},
		{"X-MediaBrowser-Token", s.Key, "", 401},
		{"Authorization", `Emby Token="` + s.Key + `"`, "", 401},
		{"", "", "?api_key=" + s.Key, 401},
	} {
		req, _ := http.NewRequest("GET", url+"/Users"+c.query, nil)
		if c.header != "" {
			req.Header.Set(c.header, c.value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s%s: %d, want %d", c.header, c.value, c.query, resp.StatusCode, c.want)
		}
	}
}

func TestItems(t *testing.T) {
	s, url := standIn(t)
	ctx := context.Background()
	c, _ := New(url, s.Key, nil)
	var all []Item
	for start := 0; ; {
		p, err := c.Items(ctx, "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c", start)
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != 1203 || p.StartIndex != start {
			t.Fatalf("page at %d: %+v", start, p)
		}
		all = append(all, p.Items...)
		start += len(p.Items)
		if len(p.Items) < PageSize {
			break
		}
	}
	if len(all) != 1203 || all[0].Path != "/media/films/Film 0000 (2001)/Film 0000 (2001).mkv" || all[0].Type != "Movie" ||
		all[0].ProviderIDs["Tmdb"] != "1000" {
		t.Fatalf("%d items, the first %+v", len(all), all[0])
	}
	d := all[0].UserData
	if d == nil || !d.Played || d.PlayCount != 0 || d.LastPlayed() != 1793564130123 || d.PositionMS() != 12345 {
		t.Fatalf("user data %+v, last played %d", d, d.LastPlayed())
	}
	q := s.Requests()[0].URL.Query()
	for k, v := range map[string]string{"recursive": "true", "includeItemTypes": "Movie,Series,Episode", "excludeLocationTypes": "Virtual",
		"fields": "Path,ProviderIds", "enableUserData": "true", "enableImages": "false", "sortBy": "SortName", "startIndex": "0",
		"limit": "500", "userId": "4a3c19a6d4ce4e1f9f1c2d3e4f5a6b7c"} {
		if q.Get(k) != v {
			t.Errorf("%s=%q, want %q", k, q.Get(k), v)
		}
	}
	// Without a user, no user data.
	p, err := c.Items(ctx, "", 1200)
	if err != nil || len(p.Items) != 3 || p.Items[0].UserData != nil || s.Requests()[len(s.Requests())-1].URL.Query().Has("enableUserData") {
		t.Fatalf("%+v %v", p, err)
	}
	if (UserData{}).LastPlayed() != 0 {
		t.Fatal("never played has a time")
	}
}

func TestUpstreamErrors(t *testing.T) {
	ctx := context.Background()
	var up *UpstreamError
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	c, _ := New(gone.URL, "key", nil)
	if _, err := c.Version(ctx); !errors.As(err, &up) {
		t.Fatalf("unreachable: %v", err)
	}
	s, url := standIn(t)
	s.Fail = http.StatusInternalServerError
	c, _ = New(url, s.Key, nil)
	if _, err := c.Users(ctx); !errors.As(err, &up) {
		t.Fatalf("a 500: %v", err)
	}
	for _, body := range []string{"<html>not json</html>", `{"ServerName": "No version"}`} {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		c, _ = New(other.URL, "key", nil)
		if _, err := c.Version(ctx); !errors.As(err, &up) {
			t.Errorf("%s: %v", body, err)
		}
		other.Close()
	}
}
