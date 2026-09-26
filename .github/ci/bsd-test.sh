#!/bin/sh
# The suite (as root) on a BSD VM, with the live package and service tests.
set -eu
cd "$(dirname "$0")/../.."
sh .github/ci/bsd-prepare.sh
if ! command -v go >/dev/null 2>&1; then
	# pkgsrc installs go under /usr/pkg/go<version>/bin.
	PATH="$(ls -d /usr/pkg/go*/bin | tail -1):$PATH"
fi
GONF_RUN_BSD_PACKAGE_TESTS=1
GONF_RUN_BSD_SERVICE_TESTS=1
export PATH GONF_RUN_BSD_PACKAGE_TESTS GONF_RUN_BSD_SERVICE_TESTS
go version
go test -count=1 ./...
