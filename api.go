// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

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
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
