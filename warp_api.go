// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// What Better Cores offers to the plugins that require it, under /peer/ (reachable only through the plugin system's
// gateway, by a plugin holding peer:better-cores): the WARP engine.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// warpEngineVersion is what a caller checks to know the calls below exist.
const warpEngineVersion = 1

// reply is the panel's envelope, the form peers and the panel's page both read.
type reply struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     any    `json:"obj"`
}

func writeEnvelope(w http.ResponseWriter, status int, obj any, err error) {
	env := reply{Success: err == nil, Obj: obj}
	if err != nil {
		env.Msg = err.Error()
	}
	writeJSON(w, status, env)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := dec.Decode(v); err != nil {
		return errors.New("bad request body")
	}
	return nil
}

// newPeerMux is the engine's API.
func newPeerMux(board *jobBoard) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /warp/info", func(w http.ResponseWriter, _ *http.Request) {
		writeEnvelope(w, http.StatusOK, map[string]any{
			"engine":       warpEngineVersion,
			"host":         warpHost,
			"ports":        warpPorts,
			"defaultPorts": defaultScanPorts,
			"peerKey":      cloudflareWarpKey,
		}, nil)
	})

	mux.HandleFunc("POST /warp/check", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			tunnelParams
			Speed bool `json:"speed"`
		}
		if err := decodeBody(w, r, &req); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil, err)
			return
		}
		if err := req.Account.validate(); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil, err)
			return
		}
		if err := req.AWG.validate(); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil, err)
			return
		}
		if req.Endpoint == "" {
			req.Endpoint = warpHost + ":2408"
		}
		if err := checkEndpoint(req.Endpoint); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil, err)
			return
		}
		id, err := board.start("check", 75*time.Second, func(ctx context.Context, j *job) (any, error) {
			j.set("check", 0, 1)
			return runCheck(ctx, req.tunnelParams, req.Speed), nil
		})
		if err != nil {
			writeEnvelope(w, http.StatusTooManyRequests, nil, err)
			return
		}
		writeEnvelope(w, http.StatusOK, map[string]string{"id": id}, nil)
	})

	mux.HandleFunc("POST /warp/scan", func(w http.ResponseWriter, r *http.Request) {
		var req scanRequest
		if err := decodeBody(w, r, &req); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil, err)
			return
		}
		if err := req.normalize(); err != nil {
			writeEnvelope(w, http.StatusBadRequest, nil, err)
			return
		}
		id, err := board.start("scan", 6*time.Minute, func(ctx context.Context, j *job) (any, error) {
			return runScan(ctx, req, j)
		})
		if err != nil {
			writeEnvelope(w, http.StatusTooManyRequests, nil, err)
			return
		}
		writeEnvelope(w, http.StatusOK, map[string]string{"id": id}, nil)
	})

	mux.HandleFunc("GET /warp/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		j, ok := board.get(r.PathValue("id"))
		if !ok {
			writeEnvelope(w, http.StatusNotFound, nil, errors.New("no such job (they are kept for twenty minutes)"))
			return
		}
		writeEnvelope(w, http.StatusOK, j, nil)
	})

	mux.HandleFunc("DELETE /warp/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !board.stop(r.PathValue("id")) {
			writeEnvelope(w, http.StatusNotFound, nil, errors.New("no such job"))
			return
		}
		writeEnvelope(w, http.StatusOK, nil, nil)
	})
	return mux
}

// peerOnly lets a request through only when the plugin system put a caller on it.
func peerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(r.Header.Get("X-LucX-Caller")) == "" {
			writeEnvelope(w, http.StatusForbidden, nil, errors.New("this API is for plugins that require Better Cores"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
