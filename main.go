// Copyright (c) 2026 AllIsA1.
// SPDX-License-Identifier: MIT

package main

import "log"

func main() {
	log.Print("better-cores plugin ready")
	if err := Serve(newMux(newJobBoard())); err != nil {
		log.Fatal(err)
	}
}
