// Copyright (C) 2019-2022 Chrystian Huot <chrystian.huot@saubeo.solutions>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>

package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"
)

// gzipWriterPool reuses gzip.Writer instances across requests since
// allocating one involves a ~256 KB internal buffer.
var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(io.Discard)
	},
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz      *gzip.Writer // nil until we decide to compress
	decided bool
}

// WriteHeader runs the compression decision as soon as we know the status
// and final headers, then delegates to the underlying writer.
func (w *gzipResponseWriter) WriteHeader(status int) {
	w.decideCompression()
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	w.decideCompression()
	if w.gz != nil {
		return w.gz.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

// decideCompression chooses (once) whether the current response should be
// gzipped, based on Content-Type. Must be called before any bytes leave the
// handler so headers are set in the right order.
func (w *gzipResponseWriter) decideCompression() {
	if w.decided {
		return
	}
	w.decided = true

	ct := w.ResponseWriter.Header().Get("Content-Type")
	if !shouldCompressContentType(ct) {
		return
	}

	// Gzipped size is unknown up-front; drop any pre-set Content-Length
	// so the stack switches to chunked encoding.
	w.ResponseWriter.Header().Set("Content-Encoding", "gzip")
	w.ResponseWriter.Header().Del("Content-Length")

	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(w.ResponseWriter)
	w.gz = gz
}

// shouldCompressContentType returns true for text-ish content. Images,
// audio, fonts, and already-gzipped archives are skipped — re-compressing
// them wastes CPU for little to no size benefit.
func shouldCompressContentType(ct string) bool {
	if ct == "" {
		// No explicit Content-Type means Go will auto-detect on Write.
		// We can't wait for that without buffering, so skip compression
		// — handlers that care about compression set Content-Type
		// explicitly before writing.
		return false
	}
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(strings.ToLower(ct))
	switch ct {
	case "application/json",
		"application/javascript",
		"application/manifest+json",
		"application/xml",
		"application/xhtml+xml",
		"application/x-font-ttf",
		"image/svg+xml",
		"text/cache-manifest",
		"text/css",
		"text/csv",
		"text/html",
		"text/javascript",
		"text/plain",
		"text/xml":
		return true
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	return false
}

// gzipHandler wraps a handler so its text responses are transparently
// gzipped when the client advertises support. Binary/audio/image responses
// pass through unchanged. Critically, the underlying gzip.Writer is only
// created (and only Close()'d) when we actually decided to compress —
// otherwise a defer'd Close() would leak an empty-gzip trailer into a
// plain response and corrupt it.
func gzipHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// WebSocket upgrades must not be intercepted.
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next(w, r)
			return
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next(w, r)
			return
		}

		w.Header().Add("Vary", "Accept-Encoding")

		wrapped := &gzipResponseWriter{ResponseWriter: w}
		defer func() {
			if wrapped.gz != nil {
				wrapped.gz.Close()
				// Reset the writer's target back to Discard so no
				// state leaks between pool uses.
				wrapped.gz.Reset(io.Discard)
				gzipWriterPool.Put(wrapped.gz)
			}
		}()

		next(wrapped, r)
	}
}
