# Changelog

Release notes for gonf. Each release is also an annotated `v*` tag; for
releases before v0.17.0 the tag message and the git log are the notes.

## v0.19.0 (2026-09-24)

Plan schema 24 (unchanged).

- `gonf plan-signer-keygen -h` prints its usage line (it printed an empty
  flag list), plus a documentation pass over every feature since v0.16.6.

Tasks and aggregates ([docs/design/tasks.md](docs/design/tasks.md), "Destination guards")
- Approved behaviour change (8h2, approved by the user 2026-09-24):
  serializable task guards (`WhenLinux`, `WhenProfile`,
  `WhenHostnameContains`, also through `WithGroupWhen`) are evaluated on the
  destination, not the controller. A pattern `Aggregate` or `AggregateTasks`
  records such a member inside its `when_begin` on every controller, so
  `gonf push paul@rocky home` from earth now includes dotfiles'
  `home_tmux_rocky` (`WhenHostnameContains("rocky")`), which it used to drop
  silently. Only opaque predicates (`When(func)`, `WhenFoo` companions) hide
  a task on the controller; a mixed task's opaque part filters there and its
  serializable part travels. Aliases and nested aggregates behave the same.
- `-list` shows such tasks with a `[destination-guarded: <guard>]` suffix
  instead of hiding them (`TaskInfo.DestinationGuard`); other rows are
  unchanged.
- A local run (`gonf home`, `Run`) is unchanged: it resolves member guards
  against this host at record time, so its plan, summaries and outcome are
  identical. `gonf plan` and push/cluster/fleet plans grow by the formerly
  dropped members, each wrapped in `when_begin`/`when_end`.
- `WhenProfile()` without profiles is now an opaque never-true predicate:
  naming such a task fails the record instead of applying it unguarded.

## v0.18.0

Plan schema 24 (unchanged); `-sealed-version` 1, `-signed-version` 1.
The v0.18.0 tag message carries no notes; this entry was added afterwards.

Signed plans ([docs/design/plan-signing.md](docs/design/plan-signing.md))
- Signing phase 2 (7g2): `gonf plan -seal -sign <signer-file>` wraps each
  sealed artifact (every host's with `-for`) in a `GONF-SIGNED-PLAN/1`
  envelope with a signed `signed-at` time; `gonf plan-signer-keygen <file>`
  creates a `0600` signer key and prints its trusted-signers line. The
  "wrote" report names the signer and its `sha256:` fingerprint. An
  envelope from v0.17.0's library-only `Sign` (no `signed-at`) never
  verifies.
- Signing phase 3 (8g2): `gonf apply` verifies a signed plan before
  decrypting it, always: `-trusted-signers` (non-root default
  `~/.config/gonf/trusted-signers`; root must pass it), `-require-signed`
  (refuse any unsigned input), `-max-signed-age` (default 24h, plus a fixed
  5-minute future skew). `gonf plan-verify` runs the same checks and writes
  the bare `plan.age` for the `age -d` emergency path. `gonf
  -signed-version` prints 1. A destination older than v0.18.0 refuses a
  signed plan.
- Signing phases 4-6 (anti-replay counter, threshold signers, an unattended
  entry point) were declined; unattended sealed apply stays blocked.

Sealed plans ([docs/design/plan-encryption.md](docs/design/plan-encryption.md))
- Behaviour change (5b2): `gonf plan -o dir` writes a plan carrying secret
  material as a sealed `dir/plan.age` by default when an operator
  recipients file exists (`~/.config/gonf/recipients` or
  `-recipients-file`); `-plaintext` opts out; a recipients file that exists
  but is unusable refuses such a plan instead of writing plaintext. Plans
  without secret material, and setups without a recipients file, are
  unchanged.

Docs
- conf-rex-gaps.md records conf's finished Rex retirement; the consumer DSL
  plan's status notes are refreshed (662).

## v0.17.0

Plan schema 24 (unchanged). Highlights since v0.16.6 (from the tag):

Sealed plans
- Phase 4 complete (yf2, zf2, 0g2): sensitive sticky-dir blobs read by
  elevated chunks of a multi-chunk push are sealed to a fresh per-push
  ephemeral age1pq key sent only on the chunk's stdin (GONF-PUSH/2) and
  decrypted into a private run dir on the destination. Requires a remote
  gonf >= 0.17.0; older remotes are refused before any upload.
- Plan signing phase 1 (6g2): plan/seal Sign/Verify, an Ed25519 envelope
  over plan.age; hardened LoadSigner/LoadTrustedSigners; weak and
  small-order trusted keys refused. No CLI wiring yet.
- -for bounds peak memory and stages artifacts before a single commit
  (qg2); recipients are printed as fingerprints, full keys with -verbose
  (4g2).

Fixes
- Multi-chunk pushes with blobs: every -apply-dir session wiped the sticky
  dir, so chunks after the upload lost their blobs (regression since
  v0.12.2, fixed in 0g2).
- NetBSD hosts: push resolves GOARCH from uname -p when uname -m names a
  port such as evbarm (found natively on the Raspberry Pis).
- Secret redaction: FlushPoint leak-free forced-flush cut for nested
  forms (sg2, tg2) with a single-pass KMP scan (0h2); RedactingWriter
  keeps one redactor per lifetime (5g2); the relay keeps its tail pending
  across the orphan deadline (zg2).
- Encode is kind-strict like decode (cg2); decode can no longer panic on
  field drift (dg2).
- CLI settings (dry-run, log level, privilege, profile, cmd timeout) are
  scoped to each invocation (vg2, xg2).
- ResetDeclarationError is an atomic take (kg2).

Internal
- All seven runner kinds inject through plan.ApplyContext (fg2); the
  internal/testseam fakes are gone.
