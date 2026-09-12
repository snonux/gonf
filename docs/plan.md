# Plan / apply (local and remote)

gonf has **one** mutation engine: a versioned JSONL **plan** of resource ops,
interpreted by `plan.Apply`. Local and remote both use it.

```text
gonf <task>…              RecordPlan → Apply          (local one-shot)
gonf plan -o dir …        RecordPlan → write plan.jsonl (+ blobs/)
gonf apply plan.jsonl     DecodePlan → Apply          (any host with gonf)
```

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
- `InstallFile` content → `content_b64` (or a blob if large).
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
| `gonf apply [-n\|-dry-run] <plan.jsonl>` | Apply a plan file (`-n` = dry-run) |

Global flags (`-profile`, `-verbose`, `-quiet`, `-dry-run` / `-n`) still apply.
`gonf -list` lists **activated** tasks (After `When*` filtering for display);
plan recording still uses the full candidate set.

Plan schema **version 2** adds `timer` and `daemon_reload` ops (this binary still
applies version 1 plans).

## JSONL sketch

```jsonl
{"op":"plan","version":1,"id":"demo"}
{"op":"link","path":"${HOME}/.bashrc","symlink":"/path/to/bashrc"}
{"op":"when_begin","all":[{"fact":"goos","eq":"linux"}]}
{"op":"file","path":"${HOME}/.taskrc","mode":"0640","content_b64":"Li4u"}
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
