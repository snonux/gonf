#!/bin/sh
# Live crontab and systemd timer tests on the GitHub runner: per-user
# crontab and user timer as the runner user, then the whole suite as root
# (root crontab, system timer, root-only code paths).
set -eu
cd "$(dirname "$0")/../.."
sudo apt-get install -y cron >/dev/null
sudo loginctl enable-linger "$(id -un)"
XDG_RUNTIME_DIR="/run/user/$(id -u)"
for _ in $(seq 30); do [ -S "$XDG_RUNTIME_DIR/bus" ] && break; sleep 1; done
DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
GONF_RUN_CRON_TESTS=1
GONF_RUN_TIMER_TESTS=1
export XDG_RUNTIME_DIR DBUS_SESSION_BUS_ADDRESS GONF_RUN_CRON_TESTS GONF_RUN_TIMER_TESTS
systemctl --user is-system-running || true

echo "== live tests as $(id -un)"
go test -count=1 -v -run 'TestLiveCronPerUser|TestLiveUserTimer' ./resource/cron/ ./resource/timer/

echo "== full suite as root"
sudo env "PATH=$PATH" GONF_RUN_CRON_TESTS=1 GONF_RUN_TIMER_TESTS=1 go test -count=1 ./...
