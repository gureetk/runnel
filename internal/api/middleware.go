// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	callerKey
)

// requestID is the id the base middleware gave this request.
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// base is the middleware every request passes through, outermost first:
// recover, request id, log (docs/02-architecture.md, section 9). The log
// line has the method, the path, the status and the time taken, and never
// the query string, which can carry a token.
func (s *Server) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var b [6]byte
		rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
		sw := &statusWriter{ResponseWriter: w}

		defer func() {
			v := recover()
			if v != nil && v != http.ErrAbortHandler {
				s.log.Error("panic", "id", id, "method", r.Method, "path", r.URL.Path,
					"panic", v, "stack", string(debug.Stack()))
				if !sw.wrote {
					s.fail(sw, r, http.StatusInternalServerError, "internal", "internal error, logged as "+id)
				}
			}
			status := sw.status
			if !sw.wrote {
				status = http.StatusOK // what net/http sends for a handler that wrote nothing
			}
			s.log.Info("request", "id", id, "method", r.Method, "path", r.URL.Path,
				"status", status, "ms", time.Since(start).Milliseconds())
			if v == http.ErrAbortHandler {
				// The handler gave up on a client that went away. Let
				// net/http close the connection quietly.
				panic(v)
			}
		}()

		next.ServeHTTP(sw, r)
	})
}

// json wraps a handler that answers in JSON: it has jsonDeadline to
// respond, after which its context ends and its writes fail.
func (s *Server) json(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not every ResponseWriter can take a deadline (a test recorder
		// cannot); without one the context still ends.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(jsonDeadline))
		ctx, cancel := context.WithTimeout(r.Context(), jsonDeadline)
		defer cancel()
		h(w, r.WithContext(ctx))
	})
}

// statusWriter remembers the status a handler sent, for the log line. It
// passes ReadFrom through, so that a file copied to the response still
// goes from disk to socket in the kernel, and Unwrap, so that
// http.ResponseController reaches the connection.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote && code >= 200 {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(r)
	}
	return io.Copy(writerOnly{w.ResponseWriter}, r)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// writerOnly hides every method but Write, so that io.Copy cannot call
// ReadFrom on it and come back here.
type writerOnly struct{ io.Writer }
