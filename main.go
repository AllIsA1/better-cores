// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"log"
	"net/http"
	"runtime"
	"time"
)

func main() {
	log.Print("better-cores plugin ready")
	var u *updater
	if panel, err := NewPanel(); err != nil {
		log.Print("the cores page is off: ", err)
	} else {
		// the panel takes a while to download a core, so that call waits longer than the others
		slow := &Panel{base: panel.base, token: panel.token, client: &http.Client{Transport: panel.client.Transport, Timeout: 6 * time.Minute}}
		u = newUpdater(panel, slow, newGithub(), runtime.GOARCH, statePath())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if u != nil {
		go u.run(ctx)
	}
	if err := Serve(newMux(newJobBoard(), u)); err != nil {
		log.Fatal(err)
	}
}
