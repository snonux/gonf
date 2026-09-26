#!/bin/sh
# Clone snonux/conf next to the gonf checkout, the layout the consumer tests
# in api/maildns_consumer_test.go look for. conf's branch named $1 is used
# when it exists, so a gonf API change can land with its conf update.
set -eu
dest="$(cd "$(dirname "$0")/../../.." && pwd)/conf"
git clone --depth 1 https://github.com/snonux/conf.git "$dest"
if [ -n "${1:-}" ] && git -C "$dest" fetch --depth 1 origin "$1" 2>/dev/null; then
	git -C "$dest" checkout -q FETCH_HEAD
fi
git -C "$dest" log --oneline -1
