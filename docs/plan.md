# Plan / apply (local and remote)

gonf has **one** mutation engine: a versioned JSONL **plan** of resource ops,
interpreted by `plan.Apply`. Local and remote both use it.

```text
gonf <task>…              RecordPlan → Apply          (local one-shot)
gonf plan -o dir …        RecordPlan → write plan.jsonl (+ blobs/)
gonf apply plan.jsonl     DecodePlan → Apply          (any host with gonf)
gonf apply -              DecodePush from stdin       (JSONL or GONF-PUSH/1)
gonf push [-n] [-id] [-- ssh…] user@host <task>…  # stream over ssh
gonf fleet [-n] [-j N] <fleet> <task>…            # parallel push to inventory
gonf hosts | fleets                               # list inventory
```

Tasks marked `Privileged()` are applied via a separate `sudo -n` / `doas` `gonf apply`
invocation (see Host `WithPrivilege`). Unprivileged tasks use plain `gonf apply`.

## Why

Tasks are Go code. Shipping whole Go task trees to every host is awkward.
Instead the controller **records** task bodies into a portable plan; the
destination only needs a gonf binary that understands the plan schema.

Local `Run` / `gonf <task>` uses the same path automatically (temp dir for
blobs, then apply, then cleanup) so local and remote cannot diverge.

## Recording (`RecordPlan`)

```go
ops, err := RecordPlan("my-plan", planDir, "home_helix", "home_tmux")
```

- Looks up **candidates** (not Activate-filtered) so serializable `When*`
  become `when_begin` / `when_end` recipes evaluated on the destination.
- Task bodies run with a draft recorder: `File` / `Dir` / `Link` / `Command` /
  … emit ops instead of applying.
- `InstallFile` content → `content_b64` when ≤ 512 KiB, else a blob sidecar.
- `SyncDir` trees → `planDir/blobs/<name>/`.
- Nested `Run` while recording (e.g. `Aggregate`) appends into the **same**
  plan; apply happens once at the top level.

### Helpers that emit recipes (not controller Stat)

| API | Plan op |
|-----|---------|
| `WhenLinux` / `WhenProfile` / `WhenHostnameContains` | `when_begin` with fact predicates |
| `WhenPathExists(path, fn)` | `when_begin` with `path_exists` around `fn` |
| `EnsureDir` | `ensure_dir` |
| `LinkIfExists` / `SymlinkMap` | `link_if_exists` |

Opaque `When(func(Facts) bool)` cannot be serialized. `RecordPlan` requires
those predicates to pass on the controller, or it errors.

## Applying (`ApplyPlan` / `plan.Apply`)

```go
if err := ApplyPlan(ops, planDir); err != nil { /* … */ }
// ApplyPlan = DetectFacts() + plan.Apply(...)
```

- First line must be `{"op":"plan","version":1,…}`. Unsupported versions are
  refused **before** any mutation.
- Streams ops top → bottom. Stackable `when_begin` / `when_end`: failed
  predicates skip the body without touching the filesystem.
- Expands `${HOME}` on the destination; unknown `${…}` is a hard error.
- Maps ops to existing resource `Ensure` helpers (`file`, `dir`, `link`,
  `cmd`, `pkg`, …) — same semantics as direct resource APIs.

## CLI

| Command | Effect |
|---------|--------|
| `gonf <task> [task…]` | Record + apply locally |
| `gonf plan [-o dir\|-stdout] [-id name] <task>…` | Write `dir/plan.jsonl` (+ `blobs/`), or print JSONL to stdout |
| `gonf apply [-n\|-dry-run] <plan.jsonl\|->` | Apply a plan file, or read **GONF-PUSH/1** / bare JSONL from stdin |
| `gonf push [-n] [-id name] [-- ssh-args…] user@host <task>…` | Record in memory, stream over `ssh` to remote `gonf apply -` |
| `gonf fleet [-n] [-j N] [-id name] <fleet> <task>…` | Resolve inventory fleet; record once; parallel push to each host |
| `gonf hosts` / `gonf fleets` | List registered inventory |

### Inventory DSL (`Host` / `Fleet`)

```go
blowfish := Host("blowfish",
    WithSSHUser("rex"), WithSSHHost("blowfish.buetow.org"), WithSSHPort(2))
fishfinger := Host("fishfinger",
    WithSSHUser("rex"), WithSSHHost("fishfinger.buetow.org"), WithSSHPort(2))
Fleet("frontends", blowfish, fishfinger) // default parallelism 5

// Later / other packages:
_ = PushHost(MustHost("blowfish"), "id")
_ = PushFleet("frontends", "base", "commons")
```

`Host` / `Fleet` auto-register. Look up with `LookupHost` / `MustHost` /
`LookupFleet` / `MustFleet`. `Fleet` takes **`HostRef` handles** (not name
strings); a host may appear **at most once** per fleet. Parallelism:
`.Parallel(n)` on the fleet handle (`n < 1` → all hosts at once).

### Privilege (Task mark + Host helper)

| Knob | API | Meaning |
|------|-----|---------|
| Whether root is needed | `Task(..., Privileged())` | Ops from that task get `elevate:true` |
| How to get root | `Host(..., WithPrivilege(PrivilegeDoas\|Sudo\|None))` or `-privilege=` | Wrap privileged apply as `doas gonf apply` / `sudo -n gonf apply` |

Default tasks are unprivileged. No auto-inference from `Package` vs `File`.
`options.WithElevate` on a `Command` elevates a single op inside an unprivileged task.

A `Run` / `push` / `fleet` that mixes both kinds **splits** the plan into ordered
chunks and runs one `gonf apply` per chunk (plain vs wrapped).

```go
Task("home_tmux", "", func() { /* … */ })
Task("pkg_openbsd", "", func() { Package("git") }, Privileged())
Host("blowfish", WithSSHUser("rex"), WithSSHHost("blowfish.buetow.org"),
    WithSSHPort(2), WithPrivilege(PrivilegeDoas))
```

### Remote push (no local disk spill)

`push` / `fleet` record into an in-memory blob store, then encode **GONF-PUSH/1**:

1. Optional gzip+tar of blobs (dirs `0700`, files `0600` on the remote staging tree)
2. Gzip of the plan JSONL

Remote `apply -` stages under `$TMPDIR/gonf-apply/<uid>/`, sweeps stale dirs on
startup, applies, then wipes the run dir. Inline content threshold is **512 KiB**
(`plan.MaxInlineContent`); larger files become blobs in the push stream.

`PushFleet` records and encodes **once**, then fans the same bytes out over SSH
in parallel (errgroup limit from the fleet or `-j`).

Example:

```text
gonf push -n user@host home_helix home_tmux
gonf push -- -p 2222 user@host home_helix
gonf fleet -n frontends base commons
gonf fleet -j 2 garage garage_deploy
```

Global flags (`-profile`, `-verbose`, `-quiet`, `-dry-run` / `-n`) still apply.
`gonf -list` lists **activated** tasks (After `When*` filtering for display);
plan recording still uses the full candidate set.

Plan schema **version 4** adds `owner` / `group` fields to the filesystem ops
(`file`, `dir`, `sync_dir`, `ensure_dir`): ownership explicitly set via
`WithOwner` / `WithGroup` is enforced on destination apply (only explicitly
configured ownership is recorded; empty fields leave ownership to the apply
side). Version 3 added `cron` and `service` ops; version 2 added `timer` and
`daemon_reload` ops (this binary still applies versions 1, 2, and 3).

## JSONL sketch

```jsonl
{"op":"plan","version":1,"id":"demo"}
{"op":"link","path":"${HOME}/.bashrc","symlink":"/path/to/bashrc"}
{"op":"when_begin","all":[{"fact":"goos","eq":"linux"}]}
{"op":"file","path":"${HOME}/.taskrc","mode":"0640","content_b64":"Li4u"}
{"op":"file","path":"${HOME}/secret.conf","mode":"0640","owner":"paul","group":"1000","content_b64":"Li4u"}
{"op":"when_end"}
{"op":"command","bin":"systemctl","args":["--user","daemon-reload"],"unless":{"bin":"true"}}
```

Schema version is the wire format version (not the gonf app version). Bump it
when ops/fields change meaning; old apply binaries reject newer plans cleanly.

## Low-level `Apply()`

`api.Apply()` / registering resources then `resource.Apply()` still exists for
unit tests and ad-hoc resource use. Normal task execution goes through the
plan engine above — do not rely on the register-then-`Apply` path for configs
you also want to run remotely.
