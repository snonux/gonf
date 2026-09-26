#!/bin/sh
# Main CI job: the full race-enabled suite as the runner user, including the
# conf consumer plan tests (conf is checked out beside gonf; the fake
# foostore reports every vault reference as not found, so the fixtures'
# synthetic secret files are used).
set -eu
cd "$(dirname "$0")/../.."
PATH="$PWD/.github/ci/fakebin:$PATH"
GONF_CONF_ROOT="$(dirname "$PWD")/conf"
export PATH GONF_CONF_ROOT
go test -race -shuffle=on -count=1 ./...
