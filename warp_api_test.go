// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type peerReply struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

func peerCall(t *testing.T, srv *httptest.Server, method, path, body, caller string) (int, peerReply) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if caller != "" {
		req.Header.Set("X-LucX-Caller", caller)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out peerReply
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestPeerAPIIsOnlyForCallersTheHostNamed(t *testing.T) {
	srv := httptest.NewServer(newMux(newJobBoard()))
	defer srv.Close()
	if code, _ := peerCall(t, srv, http.MethodGet, "/peer/warp/info", "", ""); code != http.StatusForbidden {
		t.Errorf("a call without X-LucX-Caller: %d, want 403", code)
	}
	code, rep := peerCall(t, srv, http.MethodGet, "/peer/warp/info", "", "better-warp")
	var info struct {
		Engine int   `json:"engine"`
		Ports  []int `json:"ports"`
	}
	if code != 200 || !rep.Success || json.Unmarshal(rep.Obj, &info) != nil || info.Engine != warpEngineVersion || len(info.Ports) == 0 {
		t.Fatalf("info = %d %+v %s", code, rep, rep.Obj)
	}
}

func TestPeerAPIRefusesBadRequestsBeforeStartingAnything(t *testing.T) {
	board := newJobBoard()
	srv := httptest.NewServer(newMux(board))
	defer srv.Close()
	priv, pub := newKey(t)
	acct := `{"privateKey":"` + b64(priv) + `","peerPublicKey":"` + b64(pub) + `","address":["172.16.0.2/32"]}`
	for name, tc := range map[string]struct{ path, body string }{
		"not json":              {"/peer/warp/check", `{`},
		"no account":            {"/peer/warp/check", `{"endpoint":"engage.cloudflareclient.com:2408"}`},
		"a stranger's endpoint": {"/peer/warp/check", `{"account":` + acct + `,"endpoint":"8.8.8.8:2408"}`},
		"a stranger's port":     {"/peer/warp/check", `{"account":` + acct + `,"endpoint":"162.159.192.1:22"}`},
		"too much junk":         {"/peer/warp/check", `{"account":` + acct + `,"awg":{"jc":99,"jmin":1,"jmax":2}}`},
		"scan foreign port":     {"/peer/warp/scan", `{"account":` + acct + `,"ports":[22]}`},
		"scan without account":  {"/peer/warp/scan", `{}`},
	} {
		code, rep := peerCall(t, srv, http.MethodPost, tc.path, tc.body, "better-warp")
		if code != http.StatusBadRequest || rep.Success || rep.Msg == "" {
			t.Errorf("%s: %d %+v", name, code, rep)
		}
	}
	if n := len(board.jobs); n != 0 {
		t.Errorf("%d jobs started for refused requests", n)
	}
}

func TestJobsRunReportAndCancel(t *testing.T) {
	board := newJobBoard()
	release := make(chan struct{})
	id, err := board.start("check", time.Minute, func(ctx context.Context, j *job) (any, error) {
		j.set("work", 1, 4)
		select {
		case <-release:
			return map[string]int{"n": 7}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	waitState := func(id, want string) jobView {
		t.Helper()
		for i := 0; i < 200; i++ {
			if v, _ := board.get(id); v.State == want {
				return v
			}
			time.Sleep(10 * time.Millisecond)
		}
		v, _ := board.get(id)
		t.Fatalf("job is %q, want %q", v.State, want)
		return v
	}
	var v jobView
	for i := 0; i < 200; i++ {
		if v, _ = board.get(id); v.Stage == "work" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if v.State != "running" || v.Stage != "work" || v.Done != 1 || v.Total != 4 {
		t.Errorf("progress = %+v", v)
	}
	close(release)
	done := waitState(id, "done")
	if done.Result == nil || done.Finished.IsZero() {
		t.Errorf("done job = %+v", done)
	}

	id2, _ := board.start("scan", time.Minute, func(ctx context.Context, _ *job) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if !board.stop(id2) {
		t.Fatal("stop did not find the job")
	}
	waitState(id2, "canceled")

	id3, _ := board.start("scan", 30*time.Millisecond, func(ctx context.Context, _ *job) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if v := waitState(id3, "failed"); !strings.Contains(v.Error, "too long") {
		t.Errorf("a job over its time: %+v", v)
	}

	id4, _ := board.start("check", time.Minute, func(context.Context, *job) (any, error) { return nil, errors.New("boom") })
	if v := waitState(id4, "failed"); v.Error != "boom" {
		t.Errorf("a failed job: %+v", v)
	}
	if _, ok := board.get("nope"); ok || board.stop("nope") {
		t.Error("an unknown job was found")
	}
}

func TestOnlyAFewJobsRunAtOnceAndOldOnesAreForgotten(t *testing.T) {
	board := newJobBoard()
	block := make(chan struct{})
	defer close(block)
	for i := 0; i < maxRunningJobs; i++ {
		if _, err := board.start("scan", time.Minute, func(ctx context.Context, _ *job) (any, error) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return nil, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := board.start("check", time.Minute, func(context.Context, *job) (any, error) { return nil, nil }); !errors.Is(err, errBusy) {
		t.Errorf("a job over the limit: %v", err)
	}

	old := newJobBoard()
	now := time.Now()
	old.now = func() time.Time { return now }
	id, _ := old.start("check", time.Minute, func(context.Context, *job) (any, error) { return 1, nil })
	for i := 0; i < 100; i++ {
		if v, _ := old.get(id); v.State == "done" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	now = now.Add(jobKeep + time.Minute)
	if _, err := old.start("check", time.Minute, func(context.Context, *job) (any, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := old.get(id); ok {
		t.Error("a job older than the keep time is still there")
	}
}

func TestJSONOfAJobIsStable(t *testing.T) {
	v := jobView{ID: "ab", Kind: "scan", State: "done", Result: scanResult{Tried: 3, Hits: []scanHit{{Endpoint: "162.159.192.1:2408", RttMs: 12.5}}}}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"state":"done"`, `"endpoint":"162.159.192.1:2408"`, `"rttMs":12.5`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("%s missing from %s", want, buf.String())
		}
	}
}
