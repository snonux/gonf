# Tasks, Facts, and CLI

gonf configs are Go programs: register **tasks**, then run them via `CLI()` or
`Run(...)`.

## Task

```go
Task("hello", "Say hello", func() {
    File(Home(".hello"), WithContent("hi\n"))
}, WhenLinux())
```

`Task` queues a candidate. Activation filters `When*` predicates against
[Facts](#facts), then the task body registers resources for `Apply`.

| Helper | Meaning |
|--------|---------|
| `When(pred)` | Custom `func(Facts) bool` |
| `WhenLinux()` | `GOOS == "linux"` |
| `WhenProfile("fedora", "rocky")` | Match `Facts.Profile` |
| `WhenHostnameContains("laptop")` | Substring on hostname |

Combine predicates with `And` / `Or` from [helpers.md](helpers.md).

## RegisterMethods

Reflect over exported methods on a struct. Companion methods:

- `DescFoo() string` — description for `-list`
- `WhenFoo(Facts) bool` — per-method filter

```go
type Home struct{}

func (Home) DescHelix() string { return "Install helix" }
func (Home) Helix() { /* resources */ }

RegisterMethods(Home{}, WithPrefix("home."), WithGroupWhen(
    func(f Facts) bool { return f.GOOS == "linux" },
))
```

`WithPrefix` namespaces task names; `WithGroupWhen` takes `func(Facts) bool`
predicates (not `WhenLinux()` TaskOptions).

## Aggregate

```go
Aggregate("all", "Everything matching home.*", "home\\..*")
```

Registers a task that runs every activated task whose name matches the regex.

## Facts

```go
type Facts struct {
    Profile  string // fedora | rocky | os-release ID | override
    GOOS     string
    Hostname string
}
```

Built by `DetectFacts()`. Profile comes from hostname heuristics / `/etc/os-release`,
or `-profile=...` / `SetProfileOverride`.

`ProfileIs("fedora")` is a ready-made predicate.

## CLI

```go
func main() { os.Exit(CLI()) }
```

| Flag | Meaning |
|------|---------|
| `-list` | Print activated tasks |
| `-version` | Print library version |
| `-profile=` | Override Facts.Profile before activation |
| `-dry-run` / `-n` | Preview without mutating |
| `-verbose` / `-quiet` | Log level |
| `<task>…` | Run named tasks |

`Activate(DetectFacts())` runs inside `CLI` (and `Run`) so `When*` sees the
final profile.

## Programmatic apply

```go
Activate(DetectFacts())
if err := Run("home.helix"); err != nil { /* … */ }
// Run executes matching task bodies (which register resources), then Apply.

// Or register resources yourself, then:
if err := Apply(); err != nil { /* … */ }
```

`Matching("home\\..*")` returns activated task names matching a regex
(used by `Aggregate`). `Activate` only filters candidates — it does not run
task bodies or call `Apply`.
