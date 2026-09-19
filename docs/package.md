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
Package("dtail", WithEnv(map[string]string{
    "PKG_PATH": "https://pkgrepo.example/openbsd/",
}), IsLatest)
NoPackage("oldpkg")
```

`WithEnv` overlays environment variables on every package-manager probe and
mutation. This is useful for package-manager configuration such as OpenBSD's
`PKG_PATH`; without it, package operations inherit the process environment
unchanged. The environment is preserved when recording and applying a plan.

Requires root / `doas`, same as `Service`.

See also: [service.md](service.md), [docs index](README.md).
