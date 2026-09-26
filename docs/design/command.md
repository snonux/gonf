# Command resource

Runs an external command during `Apply`. Prefer first-class resources
(`Package`, `Service`, `File`, …) when they fit; use `Command` for one-offs.

> 🦫 **Gonfy says:** A command is a stick with no shape of its own. Reach for a real resource first; when you do need a command, give it a guard so it knows when the job is done.

```go
Command("touch", List("/tmp/marker"),
    Creates("/tmp/marker"),
    WithName("touch-marker"),
)

Command("systemctl", List("daemon-reload"),
    WithName("daemon-reload"),
    DependsOn(unitFile),
)

Command("true", nil,
    Unless("test", List("-f", "/tmp/skip")),
    OnlyIf("test", List("-d", "/opt/app")),
)
```

## Options

| Option | Meaning |
|--------|---------|
| `Creates` | Skip if path exists |
| `Unless` | Skip if command exits 0 (optional `ExpectExit` / `ExpectStdout`) |
| `OnlyIf` | Run only if command exits 0 |
| `WithName` | Resource id / log label (also assigns an explicit identity to File resources) |
| `WithDir` | Working directory |
| `WithEnv` | Extra environment (`map[string]string`) |
| `DependsOn` | Ordering |

`List(...)` builds the args slice (and multi-path resource lists elsewhere).
