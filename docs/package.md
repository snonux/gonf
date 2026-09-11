# Package resource

OS-agnostic package install/remove. Backend is selected automatically:

| OS | Backend |
|----|---------|
| Linux (Fedora/RHEL/Rocky) | `dnf` |
| OpenBSD | `pkg_add` / `pkg_delete` / `pkg_info` |
| FreeBSD | `pkg` |
| NetBSD | `pkgin` (+ `pkg_info`) |

```go
Package("rsync")
Package("rsync", IsLatest)
NoPackage("oldpkg")
```

Requires root / `doas`, same as `Service`.

## Service resource

See [service.md](service.md). NetBSD uses `service(8)` and enables via `/etc/rc.conf.d/NAME`.
