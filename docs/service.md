# Service resource

OS-agnostic service/daemon management. The backend is selected automatically:

| Detected OS | Backend |
|-------------|---------|
| Linux (systemd) | `systemctl` |
| OpenBSD | `rcctl` |
| FreeBSD | `service`(8) |
| NetBSD | `service`(8) + `/etc/rc.conf.d` for enable |

```go
Service("httpd")                    // started + enabled at boot
Service("httpd", WithRestart)       // converge, then restart once
Service("httpd", WithReload)        // converge, then reload once (no restart fallback)
Service("foo", WithUser)            // systemd --user only
NoService("olddaemon")              // stopped + disabled
```

For dedicated systemd timer units, prefer [`Timer` / `NoTimer`](timer.md).

## DaemonReload

Linux systemd only. Reloads the unit manager after installing unit files:

```go
units := SyncDir(...)
DaemonReload(WithUser, DependsOn(units), IfChanged) // skip when units unchanged
DaemonReload(WithUser, DependsOn(units))            // always reload
```

`IfChanged` watches `DependsOn` targets; a `Directory[path]` dependency also
sees `File[path/…]` notes from `SyncDir` / file installs.

Requires sufficient privileges (root / `doas`), same as `Package`.

| Option | Meaning |
|--------|---------|
| `WithRestart` | Restart once when already running |
| `WithReload` | Reload once when already running (backend `reload`; Service only) |
| `WithUser` | `systemctl --user` (systemd only; rejected on BSD backends) |
| `IsAbsent` / `NoService` | Stop + disable |
| `DependsOn` | Ordering |

See also: [timer.md](timer.md) (systemd timers), [package.md](package.md), [docs index](README.md).

## Live BSD checks

Cross-compile tests on the laptop; do **not** install Go on the hosts:

| Host | GOOS/GOARCH | User | Service under test |
|------|-------------|------|--------------------|
| `f0.lan` | freebsd/amd64 | `paul` | `uptimed` |
| `fishfinger.buetow.org` | openbsd/amd64 | `rex` | `uptimed` |
| `pi0.lan` | netbsd/arm64 | `paul` | `bozohttpd` |

```bash
GOOS=netbsd GOARCH=arm64 go test -c -o /tmp/service_netbsd.test ./resource/service/
scp /tmp/service_netbsd.test paul@pi0.lan:
ssh paul@pi0.lan 'doas env PATH=/usr/sbin:/usr/pkg/bin:$PATH GONF_RUN_BSD_SERVICE_TESTS=1 ./service_netbsd.test -test.v -test.run LiveUptimed'
```
