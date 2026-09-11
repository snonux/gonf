# Service resource

OS-agnostic service/daemon management. The backend is selected automatically:

| Detected OS | Backend |
|-------------|---------|
| Linux (systemd) | `systemctl` |
| OpenBSD | `rcctl` |
| FreeBSD | `service`(8) |

```go
Service("httpd")                    // started + enabled at boot
Service("httpd", WithRestart)       // converge, then restart once
Service("foo.timer", WithUser)      // systemd --user only
NoService("olddaemon")              // stopped + disabled
```

Requires sufficient privileges (root / `doas`), same as `Package`.

## Live BSD checks

Cross-compile tests on the laptop; do **not** install Go on the hosts:

```bash
GOOS=freebsd GOARCH=amd64 go test -c -o /tmp/service_freebsd.test ./resource/service/
scp /tmp/service_freebsd.test paul@f0.lan:
ssh paul@f0.lan 'doas env GONF_RUN_BSD_SERVICE_TESTS=1 ./service_freebsd.test -test.v -test.run LiveUptimed'

GOOS=openbsd GOARCH=amd64 go test -c -o /tmp/service_openbsd.test ./resource/service/
scp /tmp/service_openbsd.test rex@fishfinger.buetow.org:
ssh rex@fishfinger.buetow.org 'doas env GONF_RUN_BSD_SERVICE_TESTS=1 ./service_openbsd.test -test.v -test.run LiveUptimed'
```

Uses the small `uptimed` daemon already present on f0 and fishfinger.
