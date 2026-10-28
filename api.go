// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// newMux serves the calls of the panel's page bridge (/api/...) and of the plugins that require this one (/peer/...).
func newMux(r *reporter, board *jobBoard) http.Handler {
	inner := http.NewServeMux()
	inner.HandleFunc("GET /status", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 20*time.Second)
		defer cancel()
		writeJSON(w, http.StatusOK, r.report(ctx))
	})
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", inner))
	mux.Handle("/peer/", peerOnly(http.StripPrefix("/peer", newPeerMux(board))))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
