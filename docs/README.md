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
