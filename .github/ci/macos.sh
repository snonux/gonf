#!/bin/sh
# macOS is not a gonf destination (the README lists Linux and the BSDs), so
# its suite is not expected to pass there: TMPDIR sits behind the /var
# symlink the key-file checks refuse, APFS rejects non-UTF-8 names, and
# users, services and systemd have no darwin backend. This job only checks
# that gonf builds and vets on darwin, e.g. as a controller on a Mac.
set -eu
cd "$(dirname "$0")/../.."
go build ./...
go vet ./...
