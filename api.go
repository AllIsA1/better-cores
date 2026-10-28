// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http"
)

// newMux serves the plugins that require this one (/peer/...). The plugin has no page and answers nothing else.
func newMux(board *jobBoard) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/peer/", peerOnly(http.StripPrefix("/peer", newPeerMux(board))))
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
