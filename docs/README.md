# Documentation index

| Doc | Topic |
|-----|--------|
| [plan.md](plan.md) | **Plan → JSONL → apply** (local one-shot and remote) |
| [tasks.md](tasks.md) | `Task`, body-vs-options concept, `RegisterMethods`, Facts/`When*`, `CLI`, `Aggregate` / `AggregateTasks`, `Alias`, `Operational` |
| [file-dir-link.md](file-dir-link.md) | `File` / `Dir` / `Link` (+ multi-path, lines, sources) |
| [config-set.md](config-set.md) | `ConfigSet`: validate a multi-file config as one staged set, member change handles |
| [command.md](command.md) | `Command` with `Unless` / `OnlyIf` / `Creates` |
| [package.md](package.md) | `Package` / `NoPackage` (dnf, pkg_add, pkg, pkgin) |
| [user.md](user.md) | additive-only local `User` accounts |
| [login-class.md](login-class.md) | `LoginClass` / `NoLoginClass` (OpenBSD `/etc/login.conf.d` fragments, OpenBSD-only requirement) |
| [service.md](service.md) | `Service` / `NoService` (systemd, rcctl, FreeBSD/NetBSD) |
| [timer.md](timer.md) | `Timer` / `NoTimer` (systemd `.timer`, Linux) |
| [systemdtimer.md](systemdtimer.md) | `SystemdTimer` / `NoSystemdTimer` (install + enable) |
| [cron.md](cron.md) | `Cron` / `NoCron` (per-user + root crontab) |
| [helpers.md](helpers.md) | Paths, sync, symlinks, predicates, `GitGlobal`, `List` |
| [options.md](options.md) | Shared options cheat-sheet |
| [secrets.md](secrets.md) | `MustSecret` / `OptionalSecret` / `ResolveSecret`, the file provider, typed errors, `SetSecretProvider` |
| [conf-rex-gaps.md](conf-rex-gaps.md) | Gaps vs `~/git/conf` Rex (remote fleet) |

## Release checklist

The version bump commit (`internal/version.go`) is easy to land on its own
and let the docs drift — that happened for v0.16.0, whose commit touched
only `internal/version.go` while [conf-rex-gaps.md](conf-rex-gaps.md) kept
citing the previous release and plan schema for another cycle (task oc2
caught and fixed it). Before or in the same commit as a version bump:

- [ ] Update the version-and-schema line(s) in
      [conf-rex-gaps.md](conf-rex-gaps.md) (near the top, the capability
      matrix heading, and the acceptance-criteria section) to the new
      `internal.Version` and `plan.CurrentVersion`.
- [ ] If `plan.CurrentVersion` moved, confirm [plan.md](plan.md) has a
      "Plan schema **version N**" paragraph for every version up to the new
      current one — a version can ship without a bump (see v23/`keyed_lines`,
      whose doc paragraph was added late by task oc2) if a doc pass is
      skipped.
- [ ] Grep the docs for "unreleased" / a feature's dev branch name and
      flip any that shipped in this release to their release note. After an
      in-place text substitution, re-read the surrounding paragraph and
      re-wrap/re-flow it — task ed2 had to repair the same orphaned-line
      artifact twice from repeated `sed` edits that left stale line breaks
      behind.
- [ ] Re-run the gate suite (`gofmt -l .`, `go build ./...`, `go vet ./...`,
      `go test -race -shuffle=on -count=1 ./...`,
      `go tool staticcheck ./...`) after the doc edits — they are
      docs-only, but confirm nothing else broke.
