// SPDX-License-Identifier: GPL-3.0-or-later

package db

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
)

// Cached is one answer of a provider, as provider_cache keeps it
// (docs/03-data-model.md, section 4).
type Cached struct {
	Status    int
	Body      []byte // uncompressed
	FetchedAt int64
}

// maxCachedBody is the most a cached answer may hold once uncompressed:
// a provider's answer larger than this is not one the server reads.
const maxCachedBody = 16 << 20

// CacheGet reads a provider's cached answer to a request, if there is one.
func (d *DB) CacheGet(ctx context.Context, provider, key string) (Cached, bool, error) {
	var c Cached
	var body []byte
	err := d.r.QueryRowContext(ctx, `SELECT status, body, fetched_at FROM provider_cache WHERE provider = ? AND key = ?`,
		provider, key).Scan(&c.Status, &body, &c.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Cached{}, false, nil
	}
	if err != nil {
		return Cached{}, false, fmt.Errorf("db: read the provider cache: %w", err)
	}
	if len(body) > 0 {
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return Cached{}, false, fmt.Errorf("db: read the provider cache: %w", err)
		}
		if c.Body, err = io.ReadAll(io.LimitReader(zr, maxCachedBody+1)); err != nil {
			return Cached{}, false, fmt.Errorf("db: read the provider cache: %w", err)
		}
		if len(c.Body) > maxCachedBody {
			return Cached{}, false, fmt.Errorf("db: a cached answer of %s is too large", provider)
		}
	}
	return c, true, nil
}

// CachePut keeps a provider's answer to a request, replacing what was
// kept for it. The body is stored gzip-compressed.
func (d *DB) CachePut(ctx context.Context, provider, key string, status int, body []byte, now int64) error {
	var buf bytes.Buffer
	if len(body) > 0 {
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		if _, err := zw.Write(body); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
	}
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO provider_cache (provider, key, fetched_at, status, body) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (provider, key) DO UPDATE SET fetched_at = excluded.fetched_at, status = excluded.status, body = excluded.body`,
			provider, key, now, status, buf.Bytes())
		return err
	})
	if err != nil {
		return fmt.Errorf("db: keep a provider's answer: %w", err)
	}
	return nil
}

// PruneCache deletes the cached answers not fetched again since a time,
// and says how many went (D53).
func (d *DB) PruneCache(ctx context.Context, before int64) (int, error) {
	var n int64
	err := d.Write(ctx, func(ctx context.Context, tx *Tx) error {
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM provider_cache WHERE fetched_at < ?`, before)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("db: prune the provider cache: %w", err)
	}
	return int(n), nil
}
