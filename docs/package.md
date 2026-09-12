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

See also: [service.md](service.md), [docs index](README.md).
