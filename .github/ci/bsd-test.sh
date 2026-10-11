#!/bin/sh
# The suite (as root) on a BSD VM, with the live package and service tests.
set -eu
cd "$(dirname "$0")/../.."
sh .github/ci/bsd-prepare.sh
if ! command -v go >/dev/null 2>&1; then
	# pkgsrc installs go under /usr/pkg/go<version>/bin.
	PATH="$(ls -d /usr/pkg/go*/bin | tail -1):$PATH"
fi
# Use go.mod's exact compiler, even when the packaged Go is newer: auto
# can produce export data that our pinned x/tools cannot read.
GOTOOLCHAIN=$(awk \
	'$1 == "go" && $2 ~ /^[0-9]+\.[0-9]+\.[0-9]+$/ { print "go" $2 }' \
	go.mod)
if [ -z "$GOTOOLCHAIN" ]; then
	echo 'bsd-test: go.mod must declare an exact Go release (go X.Y.Z)' >&2
	exit 1
fi
if ! command -v git >/dev/null 2>&1; then
	GOFLAGS=-buildvcs=false
	export GOFLAGS
fi
GONF_RUN_BSD_PACKAGE_TESTS=1
GONF_RUN_BSD_SERVICE_TESTS=1
if [ "$(uname -s)" = NetBSD ]; then
	# pi0.lan runs pkgsrc's bozohttpd; the VM has the same daemon in base
	# as httpd.
	GONF_LIVE_SERVICE=httpd
	export GONF_LIVE_SERVICE
fi
export PATH GOTOOLCHAIN GONF_RUN_BSD_PACKAGE_TESTS GONF_RUN_BSD_SERVICE_TESTS
go version
go test -count=1 ./...
