# Design notes

Long-form design notes, background and history behind gonf's features.
The terse quick reference is [../reference.md](../reference.md).

## Documentation index

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
| [secrets.md](secrets.md) | `MustSecret` / `OptionalSecret` / `ResolveSecret`, the file provider, typed errors, `SetSecretProvider`, `secret.NewFallback`, the foostore provider |
| [plan-encryption.md](plan-encryption.md) | Sealed plans: `gonf plan -seal` / `-for`, `gonf apply -identity`, the recipients file, sealing by default, sealed multi-chunk blobs |
| [plan-signing.md](plan-signing.md) | Signed plans: `gonf plan -seal -sign`, `gonf plan-signer-keygen`, `gonf apply -trusted-signers` / `-require-signed`, `gonf plan-verify` |
| [conf-rex-gaps.md](conf-rex-gaps.md) | Gaps vs `~/git/conf` Rex (remote fleet) |
| [consumer-dsl-simplification-plan.md](consumer-dsl-simplification-plan.md) | Consumer DSL simplification plan and its task status |
| [../../CHANGELOG.md](../../CHANGELOG.md) | Release notes (also in each release's annotated tag) |

## Release checklist

The version bump commit (`internal/version.go`) is easy to land on its own
and let the docs drift — that happened for v0.16.0, whose commit touched
only `internal/version.go` while [conf-rex-gaps.md](conf-rex-gaps.md) kept
citing the previous release and plan schema for another cycle (task oc2
caught and fixed it). It happened again for v0.16.5 (task se2): commit
`8405f57` bumped the version and was immediately tagged `v0.16.5`, and only
a *later*, separate commit `79af441` refreshed
[conf-rex-gaps.md](conf-rex-gaps.md) — so `git show v0.16.5:docs/conf-rex-gaps.md`
permanently reads "Refreshed ... against gonf v0.16.4", the exact drift oc2's
checklist exists to prevent. Contrast the very next release, v0.16.6
(`deb324a`): the version bump and the `conf-rex-gaps.md` refresh landed in
that ONE commit, so the tag it carries is accurate. `deb324a`'s pattern —
not "before or in the same commit" — is the model to follow. The checklist
was skipped again for v0.18.0: its bump commit `8fb7a4e` touched only
`internal/version.go` and was tagged as is, so the v0.18.0 tag predates the
refresh of [conf-rex-gaps.md](conf-rex-gaps.md) to v0.18.0 (the tagged copy
still reads v0.17.0), and its tag message carries no release notes (the
v0.18.0 entry in [CHANGELOG.md](../../CHANGELOG.md) was added afterwards). The
tag is already in the Go module proxy and stays where it is:

- [ ] **Fold the version-and-schema doc update into the SAME commit as the
      version bump** (like `deb324a`, not the two-commit `8405f57`/`79af441`
      split that produced v0.16.5's drift). Update the version-and-schema
      line(s) in [conf-rex-gaps.md](conf-rex-gaps.md) (near the top, the
      capability matrix heading, and the acceptance-criteria section) to the
      new `internal.Version` and `plan.CurrentVersion`.
- [ ] Add the release's entry to [CHANGELOG.md](../../CHANGELOG.md) in the
      same commit, and use it as the annotated tag's message.
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
- [ ] **Tag only after the doc edits have landed on the branch being
      tagged.** If the doc refresh ever does end up in a separate, later
      commit despite the rule above, do NOT create the version tag until
      that doc commit has also landed — the tag must point at a commit
      where `git show <tag>:docs/design/conf-rex-gaps.md` already shows the new
      version. A tag created at the bump commit (before the doc commit
      exists) is immutable once pushed and cannot be repaired afterward,
      which is exactly how v0.16.5 shipped with stale docs baked into its
      tag permanently.
