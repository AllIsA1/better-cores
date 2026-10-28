// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// newMux serves the plugins that require this one (/peer/...) and, when there is an updater, the panel's Cores page
// (/api/cores...). The plugin has no page of its own and answers nothing else.
func newMux(board *jobBoard, u *updater) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/peer/", peerOnly(http.StripPrefix("/peer", newPeerMux(board))))
	if u != nil {
		mux.Handle("/api/", http.StripPrefix("/api", panelUser(newCoresMux(u))))
	}
	return mux
}

// panelUser lets through what the panel forwards for a logged-in administrator: it sets X-LucX-User itself and clients cannot forge it.
func panelUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestUser(r) == "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"success": false, "msg": "only through the panel"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// newCoresMux is what the Cores page of the panel talks to:
//
//	GET  /cores               what is known: the last look, each core's latest release and what this plugin installed
//	POST /cores/check         look at the releases now (answers when the look is over)
//	POST /cores/<id>/install  put the last release of one core in place of the panel's (answers at once; poll GET /cores)
func newCoresMux(u *updater) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cores", func(w http.ResponseWriter, _ *http.Request) {
		writeOK(w, http.StatusOK, u.view())
	})
	mux.HandleFunc("POST /cores/check", func(w http.ResponseWriter, r *http.Request) {
		if err := u.check(r.Context()); err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, errCheckRunning) {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]any{"success": false, "msg": err.Error()})
			return
		}
		writeOK(w, http.StatusOK, u.view())
	})
	mux.HandleFunc("POST /cores/{id}/install", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		if err := u.install(id); err != nil {
			status := http.StatusConflict
			if errors.Is(err, errUnknownCore) {
				status = http.StatusNotFound
			}
			writeJSON(w, status, map[string]any{"success": false, "msg": err.Error()})
			return
		}
		writeOK(w, http.StatusAccepted, u.view())
	})
	return mux
}

// writeOK answers in the panel's envelope, which the panel's pages already know how to read.
func writeOK(w http.ResponseWriter, status int, obj any) {
	writeJSON(w, status, map[string]any{"success": true, "msg": "", "obj": obj})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
