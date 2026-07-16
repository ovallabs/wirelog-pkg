#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "gofmt needed on:" >&2
	echo "$unformatted" >&2
	exit 1
fi

go vet ./...

if command -v golangci-lint >/dev/null 2>&1; then
	golangci-lint run ./...
fi

go test -cover ./...
