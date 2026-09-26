#!/bin/sh
# Fedora container (root): the full suite as a regular user, then the live
# dnf round trip (install/upgrade/remove tig) as root.
set -eu
cd "$(dirname "$0")/../.."
# systemd provides systemctl, which the timer unit tests expect on Linux.
dnf install -y golang git rpm findutils diffutils which systemd >/dev/null
id tester >/dev/null 2>&1 || useradd -m tester
chown -R tester .
echo "== full suite as tester"
runuser -u tester -- env HOME=/home/tester USER=tester sh -c "cd '$PWD' && go test -count=1 ./..."
echo "== live dnf tests as root"
GONF_RUN_DNF_TESTS=1 go test -count=1 -v -run 'TestApplyDNF|TestDetectPackageManager' ./resource/pkg/
