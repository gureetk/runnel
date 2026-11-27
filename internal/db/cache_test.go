// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"bytes"
	"context"
	"testing"
)

func TestProviderCache(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	body := bytes.Repeat([]byte(`{"title": "An Invented Film"}`), 100)
	if err := d.CachePut(ctx, "tmdb", "/movie/101", 200, body, 10); err != nil {
		t.Fatal(err)
	}
	if err := d.CachePut(ctx, "tmdb", "/movie/999", 404, nil, 20); err != nil {
		t.Fatal(err)
	}
	got, found, err := d.CacheGet(ctx, "tmdb", "/movie/101")
	if err != nil || !found || got.Status != 200 || !bytes.Equal(got.Body, body) || got.FetchedAt != 10 {
		t.Fatalf("%v %v %+v", found, err, got)
	}
	var stored int
	if err := d.r.QueryRowContext(ctx, `SELECT length(body) FROM provider_cache WHERE key = '/movie/101'`).Scan(&stored); err != nil || stored >= len(body)/4 {
		t.Errorf("%d bytes kept for %d, %v: not compressed", stored, len(body), err)
	}
	// A newer answer replaces the older.
	if err := d.CachePut(ctx, "tmdb", "/movie/101", 404, nil, 30); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := d.CacheGet(ctx, "tmdb", "/movie/101"); got.Status != 404 || got.Body != nil || got.FetchedAt != 30 {
		t.Errorf("%+v", got)
	}
	if _, found, _ := d.CacheGet(ctx, "other", "/movie/101"); found {
		t.Error("one provider's answer is found under another")
	}
	if n, err := d.PruneCache(ctx, 25); err != nil || n != 1 {
		t.Errorf("pruned %d, %v", n, err)
	}
	if _, found, _ := d.CacheGet(ctx, "tmdb", "/movie/999"); found {
		t.Error("an answer older than the time is kept")
	}
}

func TestMetadataKey(t *testing.T) {
	ctx := context.Background()
	d, _ := openTemp(t)
	if key, s, err := d.MetadataKey(ctx); err != nil || key != "" || s.OK != nil || s.CheckedAt != nil {
		t.Fatalf("%q %+v %v", key, s, err)
	}
	invented := "an-invented-key-0123456789"
	if err := d.SetMetadataKey(ctx, &invented, 100); err != nil {
		t.Fatal(err)
	}
	if key, s, _ := d.MetadataKey(ctx); key != invented || !*s.OK || *s.CheckedAt != 100 {
		t.Errorf("%q %+v", key, s)
	}
	if err := d.SetKeyStatus(ctx, false, 200); err != nil {
		t.Fatal(err)
	}
	if _, s, _ := d.MetadataKey(ctx); *s.OK || *s.CheckedAt != 200 {
		t.Errorf("%+v", s)
	}
	if err := d.SetMetadataKey(ctx, nil, 300); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := d.r.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key LIKE 'tmdb_key%'`).Scan(&left); err != nil || left != 0 {
		t.Errorf("%d left after removing the key, %v", left, err)
	}
}
