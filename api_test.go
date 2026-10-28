// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOnlyThePeerCallsAreServed(t *testing.T) {
	srv := httptest.NewServer(newMux(newJobBoard(), nil))
	defer srv.Close()
	for _, bad := range []string{"/api/status", "/ui/index.html", "/ui/../go.mod"} {
		resp, err := http.Get(srv.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s answered 200", bad)
		}
	}
}
