// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

// Package main: lucxplugin.go holds what every LucX plugin needs. Copy it into
// your plugin and keep it next to main.go.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Serve listens on the unix socket the panel assigned ($LUCX_SOCKET), answers
// GET /_lucx/health itself and sends everything else to handler. It returns
// after SIGTERM/SIGINT once in-flight requests finished.
func Serve(handler http.Handler) error {
	socket := os.Getenv("LUCX_SOCKET")
	if socket == "" {
		return errors.New("LUCX_SOCKET is not set: run this plugin through the panel")
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_lucx/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", handler)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Event is a panel event the panel delivers to POST /_lucx/events. Subscribe
// by listing events:<name> permissions in lucx-plugin.json.
type Event struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Source string          `json:"source"`
	Time   time.Time       `json:"time"`
	Data   json.RawMessage `json:"data"`
}

// EventHandler decodes a delivered event and hands it to fn. Answer quickly:
// the panel waits at most a few seconds and does not retry.
func EventHandler(fn func(Event)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev Event
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&ev); err != nil {
			http.Error(w, "bad event", http.StatusBadRequest)
			return
		}
		fn(ev)
		w.WriteHeader(http.StatusNoContent)
	})
}

// PatchOp is one RFC 6902 operation. The panel accepts add, remove, replace and test.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// PipelineHandler serves POST /_lucx/pipeline/<name>: fn receives the current
// document and returns the operations to apply (none means no change). The
// panel runs this on every config generation, so fn must be idempotent.
func PipelineHandler(fn func(doc json.RawMessage) ([]PatchOp, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			http.Error(w, "bad document", http.StatusBadRequest)
			return
		}
		ops, err := fn(doc)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(ops) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"patch": ops})
	})
}

// SeamCall is what the panel asks of a seam: where the call came from and the HTTP request, and for an
// "after" seam the answer the panel gave. Declare seams in lucx-plugin.json and serve them with SeamHandler.
type SeamCall struct {
	Seam struct {
		Engine string `json:"engine"` // "api" or "sub"
		Mode   string `json:"mode"`   // "before", "replace" or "after"
		Route  string `json:"route"`  // the route you declared, e.g. "/server/*"
	} `json:"seam"`
	Request  SeamRequest   `json:"request"`
	Response *SeamResponse `json:"response,omitempty"` // only for "after"
}

// SeamRequest is the request the panel is handling. The method, route and path are the panel's and cannot
// be changed; you may change the query, the body and the headers the panel lets through (never Cookie,
// Authorization or the CSRF token: you do not see those either).
type SeamRequest struct {
	Method  string              `json:"method"`
	Route   string              `json:"route"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Params  map[string]string   `json:"params,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
	User    string              `json:"user,omitempty"` // the logged-in panel user (API only)
}

// SeamResponse is an answer: the panel's (in "after") or the one you produce.
type SeamResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

// SeamReply is your answer to a seam. Build it with Continue, Respond, Fallback, Keep or Modify.
type SeamReply struct {
	Action   string          `json:"action"`
	Request  *RequestChange  `json:"request,omitempty"`
	Response *ResponseChange `json:"response,omitempty"`
}

// RequestChange lists only what you change in a request. A nil field stays as it is; in Headers an empty
// list removes the header.
type RequestChange struct {
	Query   *string             `json:"query,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    *[]byte             `json:"body,omitempty"`
}

// ResponseChange lists only what you change in an answer.
type ResponseChange struct {
	Status  *int                `json:"status,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    *[]byte             `json:"body,omitempty"`
}

// Continue lets the panel carry on with the request, changed as given (nil: unchanged). "before" only.
func Continue(change *RequestChange) SeamReply { return SeamReply{Action: "continue", Request: change} }

// Respond answers the request instead of the panel ("before" and "replace"). The status must be 200-599.
func Respond(status int, headers map[string][]string, body []byte) SeamReply {
	return SeamReply{Action: "respond", Response: &ResponseChange{Status: &status, Headers: headers, Body: &body}}
}

// Fallback hands a "replace" seam back to the panel's own code.
func Fallback() SeamReply { return SeamReply{Action: "fallback"} }

// Keep leaves the panel's answer as it is ("after" only).
func Keep() SeamReply { return SeamReply{Action: "keep"} }

// Modify changes the panel's answer ("after" only).
func Modify(change ResponseChange) SeamReply { return SeamReply{Action: "modify", Response: &change} }

// Str and Bytes make the pointers the change types use.
func Str(s string) *string { return &s }

// Bytes returns a pointer to b for RequestChange.Body and ResponseChange.Body.
func Bytes(b []byte) *[]byte { return &b }

// SeamHandler serves POST /_lucx/seam/{before|replace|after}. Answer within a few hundred milliseconds:
// the panel waits at most that long and then runs its own code as if you were not there. An error or an
// invalid reply is treated the same way, and a plugin that keeps failing is skipped
// for a while.
func SeamHandler(fn func(SeamCall) (SeamReply, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call SeamCall
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&call); err != nil {
			http.Error(w, "bad seam call", http.StatusBadRequest)
			return
		}
		reply, err := fn(call)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})
}

// RequestUser is the panel user on whose behalf the request was made. The
// panel sets it itself; clients cannot forge it.
func RequestUser(r *http.Request) string { return r.Header.Get("X-LucX-User") }

// StateDir is the only directory the plugin may write to.
func StateDir() string {
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" {
		return dir
	}
	return os.Getenv("LUCX_DATA_DIR")
}

// Panel calls the panel API with the token the panel issued to this plugin.
// Only routes covered by the permissions in lucx-plugin.json answer; others return 403.
type Panel struct {
	base   string
	token  string
	client *http.Client
}

// NewPanel reads $LUCX_PANEL_URL and the credential file the panel provides.
func NewPanel() (*Panel, error) {
	base := os.Getenv("LUCX_PANEL_URL")
	dir := os.Getenv("CREDENTIALS_DIRECTORY")
	if base == "" || dir == "" {
		return nil, errors.New("no panel address or token: run this plugin through the panel")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "panel-token"))
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{}
	if u.Scheme == "https" {
		// The panel's certificate is issued for its public name, not for the
		// address we use; the connection stays on this machine or a trusted network.
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	}
	return &Panel{base: strings.TrimRight(base, "/"), token: strings.TrimSpace(string(raw)), client: &http.Client{Transport: transport, Timeout: 15 * time.Second}}, nil
}

type envelope struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

// Do calls /panel/api<path> and decodes the "obj" of the panel's answer into out.
func (p *Panel) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+"/panel/api"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("panel answered HTTP %d for %s %s", resp.StatusCode, method, path)
	}
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if !env.Success {
		return errors.New(env.Msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Obj, out)
}
