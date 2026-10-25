// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"embed"
	"encoding/json"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed ui
var pages embed.FS

// newMux serves the page and the calls it makes. The panel's page bridge only reaches /api/...
func newMux(r *reporter) http.Handler {
	inner := http.NewServeMux()
	inner.HandleFunc("GET /status", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, r.report(ctx))
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", inner))
	mux.Handle("/ui/", pageHandler())
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// pageHandler answers GET /ui/... with the embedded files; a path ending in / serves its index.html.
func pageHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		clean := path.Clean(name)
		if !strings.HasPrefix(clean, "ui/") {
			http.NotFound(w, r)
			return
		}
		data, err := pages.ReadFile(clean)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		typ := mime.TypeByExtension(path.Ext(clean))
		if typ == "" {
			typ = "application/octet-stream"
		}
		w.Header().Set("Content-Type", typ)
		_, _ = w.Write(data)
	})
}
