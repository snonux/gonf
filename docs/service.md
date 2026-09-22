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

For a restart or reload only when managed input changed, use `OnChange`; it
adds the required ordering edge too, while preserving normal enable/start
convergence:

```go
conf := InstallFile("/etc/httpd.conf", "httpd.conf")
Service("httpd", WithRestart, OnChange(conf))
```

For dedicated systemd timer units, prefer [`Timer` / `NoTimer`](timer.md).

`Run`, `push`, `apply`, and `fleet` all go through the one apply engine:
Service records a `service` plan op (name, absent, restart/reload, user) that
is applied like any other resource (see [plan.md](plan.md)).

## DaemonReload

Linux systemd only. Reloads the unit manager after installing unit files:

```go
units := SyncDir(...)
DaemonReload(WithUser, DependsOn(units), IfChanged) // skip when units unchanged
DaemonReload(WithUser, DependsOn(units))            // always reload
DaemonReload(WithUser, OnChange(units))              // preferred change gate + ordering
```

`IfChanged` watches `DependsOn` targets (it records the same op as
`OnChange(units)`); a `Directory[path]` dependency also sees `File[path/…]`
notes from `SyncDir` / file installs. A reload left with `IfChanged` but
neither `DependsOn` nor `WithWatch` ids (and no same-bus declaration to
merge with) could never reload; the plan pre-flight refuses it.

There is one daemon-reload per bus and recipe scope (a task body or a
when-fragment): its ID is `DaemonReload[system]` or `DaemonReload[user]`. A
second declaration on the same bus in the same scope, whether a further
`DaemonReload` or a `SystemdUnits` composition, merges into the first one and
returns it. The merged reload applies after the inputs of every declaration
and watches all of them. A later declaration's watched ids count as inputs
here even when it only watches them (`WatchChanges`, or the legacy
`WithWatch` + `IfChanged`). So do the files under a directory such a later
declaration watches: a watched `Directory[p]` also fires on `File[p/…]`
changes, so the merged reload runs after every file under `p` that is
registered before that declaration. The first declaration's own watches get
no such extra ordering (a single declaration never had it either): declare
files under a watched directory before the composition that watches it. It stays change-gated
only when every declaration is gated, because an unconditional reload wins
over a gated one. The recorded op keeps its first position, and apply orders
it after the later inputs through its deps. Declarations on different buses
stay separate.

Requires sufficient privileges (root / `doas`), same as `Package`.

| Option | Meaning |
|--------|---------|
| `WithRestart` | Restart once when already running |
| `WithReload` | Reload once when already running (backend `reload`; Service only) |
| `WithUser` | `systemctl --user` (systemd only; rejected on BSD backends) |
| `IsAbsent` / `NoService` | Stop + disable |
| `DependsOn` | Ordering |

### Composing units with SystemdUnits

`SystemdUnits(FanIn(inputs…), ActivateTimer(…)/ActivateService(…), [WithUserBus()])`
declares that bus's reload for you, watching the `FanIn` inputs. Each
activation depends on the reload and watches only its own composition's
inputs. You can call `SystemdUnits` several times per scope and bus. The
reload is shared (see above), so one changed input reloads systemd once and
restarts only the units whose composition declared that input. Because the
activations depend on the shared reload, the activations of the first
composition also wait for the inputs of every later composition on that bus.
They converge in the same apply, just later in the order. A single
composition records exactly the same plan as before.

A merge is refused with a fail-fast `DaemonReload[…]: cannot merge …` error
that names both watch lists, in these cases:

- The earlier declaration sits on the other side of a when-block boundary
  (e.g. inside `WhenPathExists` / `WhenHostname`). The merged reload would be
  skipped wherever that block is inactive. A GOOS requirement block (the one
  `LoginClass` records, for example) counts as a when-block too, even though
  it keeps the recipe scope. Any `when_begin` / `when_end` recorded between
  the two declarations blocks the merge.
- The new declaration depends on the reload directly, for example
  `SystemdUnits(FanIn(unitsA), …)` or `DaemonReload(DependsOn(unitsA))`,
  where `unitsA` is an earlier composition on the same bus. The reload would
  depend on itself.
- The earlier declaration sits on the other side of a privilege change, for
  example when it came from a `Privileged` task run through a nested `Run`.
  The merged op would belong to neither privilege chunk and could not see the
  other chunk's change reports.
- A new input already depends on the reload, for example
  `b := InstallFile(…, DependsOn(unitsA))` followed by
  `SystemdUnits(FanIn(b), …)`. `unitsA` contains the reload, so the merge
  would create a dependency cycle that no host can apply. This includes
  files under a directory the new declaration watches: a
  `File[/etc/foo/x.conf]` that depends on `unitsA` makes
  `DaemonReload(OnChange(Dir("/etc/foo")))` a cycle, because the merged
  reload would have to run after that file.

Declare the compositions in the same block and privilege scope, without
making one's inputs depend on the other. You can also pass every input to one
`SystemdUnits` `FanIn`.

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
