# Copyright (c) 2026 AllIsA1.
# SPDX-License-Identifier: MIT

# Builds the binaries the panel installs: bin/<arch>/$(NAME) for each supported CPU.
# Commit bin/ — the panel downloads your repository as it is, it does not compile anything.
NAME := bettercores
ARCHES := amd64 arm64

build:
	@for arch in $(ARCHES); do \
		echo "building bin/$$arch/$(NAME)"; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "-s -w" -o bin/$$arch/$(NAME) . || exit 1; \
	done

test:
	go test ./...

.PHONY: build test
