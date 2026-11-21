// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Writers are kept between requests. At BestSpeed a writer holds a
// fraction of the memory a default one does, and JSON still shrinks well.
var gzipWriters = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

// gzipWriter compresses what a JSON handler writes.
type gzipWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer
	started bool
	plain   bool // a status with no body: nothing to compress
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.started {
		return
	}
	g.started = true
	if code == http.StatusNoContent || code == http.StatusNotModified {
		g.plain = true
	} else {
		h := g.Header()
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if !g.started {
		g.WriteHeader(http.StatusOK)
	}
	if g.plain {
		return g.ResponseWriter.Write(p)
	}
	return g.gz.Write(p)
}

func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// gzipJSON compresses a JSON response when the request allows it. Media
// is never compressed (docs/04-api.md, section 1).
func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzipWriters.Get().(*gzip.Writer)
		gz.Reset(w)
		g := &gzipWriter{ResponseWriter: w, gz: gz}
		defer func() {
			if g.started && !g.plain {
				gz.Close()
			}
			gzipWriters.Put(gz)
		}()
		next.ServeHTTP(g, r)
	})
}
