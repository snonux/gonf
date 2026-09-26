#!/bin/sh
# remote_smoke: a real gonf push over ssh to localhost, with doas for the
# privileged task.
set -eu
cd "$(dirname "$0")/../.."
me="$(id -un)"
sudo apt-get install -y openssh-server opendoas >/dev/null
echo "permit nopass $me" | sudo tee /etc/doas.conf >/dev/null
sudo chmod 0400 /etc/doas.conf
sudo systemctl start ssh
mkdir -p ~/.ssh && chmod 700 ~/.ssh
[ -f ~/.ssh/id_ed25519 ] || ssh-keygen -q -t ed25519 -N '' -f ~/.ssh/id_ed25519
cat ~/.ssh/id_ed25519.pub >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
chmod go-w ~
ssh-keyscan -H localhost >> ~/.ssh/known_hosts 2>/dev/null
ssh -o BatchMode=yes localhost 'doas -n true' && echo "ssh + doas to localhost ok"
GONF_REMOTE_HOST="$me@localhost" GONF_REMOTE_PRIVILEGE=doas \
	go test -tags remote_smoke -count=1 -v -run RemoteSmoke ./internal/remote/
