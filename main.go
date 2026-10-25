// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"log"
)

func main() {
	var api panelAPI
	if p, err := NewPanel(); err != nil {
		log.Printf("better-cores: %v; no core status can be read", err)
		api = failingPanel{err}
	} else {
		api = p
	}
	log.Print("better-cores plugin ready")
	if err := Serve(newMux(newReporter(api, newGithub()))); err != nil {
		log.Fatal(err)
	}
}

type failingPanel struct{ err error }

func (f failingPanel) Do(context.Context, string, string, any, any) error { return f.err }
