// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheStatusCallIsServedAndThereIsNoPage(t *testing.T) {
	r, _, _ := rig()
	srv := httptest.NewServer(newMux(r, newJobBoard()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []coreStatus
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&list) != nil || len(list) != len(cores) {
		t.Fatalf("status = %d, %d cores", resp.StatusCode, len(list))
	}
	for _, bad := range []string{"/api/install", "/ui/index.html", "/ui/../go.mod"} {
		resp, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Errorf("%s answered 200", bad)
		}
	}
}
