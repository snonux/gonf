# Sealed plan artifacts (design, task w82)

Status: **design only, not implemented.** Nothing below exists in gonf yet.
Implementation needs this design accepted and the user's explicit approval;
the follow-up tasks are listed in the last section.

**Merge order.** This design builds on task 062 (secret-aware plans, plan
schema v22), which is not merged into main yet (branch
`worktree-agent-aa9271605948d966b`). The secrets.md sections cited below
("What reaches the plan", "Limits of the scan", "Where a sensitive plan
goes", "Retention and cancellation", "Not provided") and the plan.md
schema-22 text exist only on that branch until it lands; this document is
meant to be read, and its tasks started, after 062 is merged. 062 marks
secret-bearing ops `sensitive`, keeps `plan.jsonl` plaintext under
private-filesystem protections, refuses `gonf plan -stdout` for such plans
unless `-with-secrets`, and states that durable encryption needs its own
design. This is that design. It answers F10 / P8 of
[consumer-dsl-simplification-plan.md](consumer-dsl-simplification-plan.md)
("If durable artifacts are required, design recipient encryption or a
protected sidecar before claiming encrypted plans").

**Summary.** `gonf plan -o dir -seal` writes one file, `dir/plan.age`: the
existing GONF-PUSH/1 frame (plan JSONL plus blobs, exactly what `push`
streams) encrypted with [age](https://age-encryption.org/v1) to hybrid
post-quantum recipients (`age1pq…`). `gonf apply -identity key
dir/plan.age` (or `-` on stdin) decrypts it and applies it like a plain
`gonf apply` of a plan file: one process, one privilege. Push, cluster and
fleet do not change. Keys are ordinary age key pairs (`age-keygen -pq`); a
destination holds its own private key, never a secret store credential.
**Sealing gives confidentiality at rest and nothing else: a plan that
decrypts proves nothing about who made it.**

## What travels where today

| Path | Plaintext copies of secret material | Protection (after 062) |
|------|-------------------------------------|------------------------|
| `gonf plan -o dir` | `dir/plan.jsonl` and `dir/blobs/` until the operator deletes them; the default `dir` is `.`, normally the recipe checkout | `plan.jsonl` `0600`, `blobs/` `0700`, owner/writability-checked `dir` (plan.md "The output directory"), stderr warning naming the sensitive ops |
| `gonf plan -stdout -with-secrets` | wherever stdout goes | explicit operator export; refused without the flag |
| `gonf <task>` (local run, `api.Run`) | private `$TMPDIR` plan directory with blobs; removed on return | `0700` dir; left behind only on SIGKILL/crash |
| Local elevated re-exec (`api/apply_chunks.go` `defaultElevatedApply`, reached only from `Run` via `ApplyChunksContext`) | `chunk-elevated.jsonl` written `0600` into that same private plan directory, read by the sudo/doas child | same directory, removed on return |
| `gonf apply <plan.jsonl>` | the operator's file | `api.ApplyPlan` → `plan.Apply` in one process: **no privilege split and no re-exec**; the operator runs it as the user that must apply every op (e.g. `sudo gonf apply`) |
| `push`, `cluster`, `fleet` | controller memory (`plan.MemoryStore`), SSH stdin (GONF-PUSH/1, `plan/pushwire.go`), destination run dir `$TMPDIR/gonf-apply/<uid>/run-*` | SSH in transit; owner-only run dir, removed after the apply |
| Destination leftovers | a run dir of a killed apply (SIGKILL, crash, power loss) | swept only lazily: `plan.NewApplyRunDir` of the **same uid** removes run dirs older than 24 h (`plan/staging.go`). A killed root apply leaves plaintext under root's `$TMPDIR/gonf-apply/0` until a later root apply more than 24 h after it |
| Multi-chunk push with blobs (`internal/remote/delivery.go`) | sticky `/tmp/gonf-apply-sticky-<plan>-<host>` owned by the SSH login user | removed after success only; 062 refuses sensitive blobs in an elevated chunk before any SSH traffic |
| Destination | the managed files themselves, validation candidates | by design (the destination must write them); candidates are private and removed |

## Threat model

Assets: the secret payloads of sensitive ops (`content_b64`, blobs,
`template_data`, member contents, argv, environment), plus anything the 062
scan misses (secrets.md "Limits of the scan": transformed values, synced
trees, weak secrets in identities, non-UTF-8 template strings).

| # | Adversary / exposure | Today | With sealing |
|---|----------------------|-------|--------------|
| T1 | Another local account on the controller | blocked by `0600`/`0700` and the dir rule | unchanged (still blocked) |
| T2 | **Copies of the artifact outside gonf's control**: backups and snapshots of the checkout (`-o .`), Syncthing/Dropbox, `git add -A` of a stray `plan.jsonl`, CI artifact uploads, a plan copied to a host with scp and forgotten | plaintext, readable by whoever reads the copy | ciphertext; readable only with a recipient's private key. **This is the threat sealing exists for.** |
| T3 | Disk theft or disposal of controller / CI runner | plaintext unless the disk is encrypted | ciphertext |
| T4 | Harvest now, decrypt later (a durable backup read years later) | n/a | mitigated by the hybrid ML-KEM-768 + X25519 recipient type, the only one accepted |
| T5 | Compromise of a recipient's private key later | n/a | **no forward secrecy**: every artifact ever sealed to that key, wherever a copy survives, opens. Bounded only by retention and rotation (below) |
| T6 | Network observer on push | SSH | unchanged; no second layer (below) |
| T7 | Destination staging after decryption | n/a | a sealed apply with blobs extracts them into a run dir like push; a killed apply leaves them until the lazy sweep (see "Plaintext after decryption") |
| T8 | Local elevated re-exec | private dir of the controller user, short-lived; root reads it anyway | not reached: a sealed apply does not privilege-split |
| T9 | Same-user malware on the controller, root on the destination | can read the secret store, the identity and the written files | **out of scope**; no file format helps |
| T10 | **Forgery / substitution** of a plan a later `gonf apply` trusts | directory protections only | **unchanged.** Recipient public keys are public, and age has no sender authentication, so anyone can produce a `plan.age` that decrypts. Trust still comes only from where the file is stored and who can write there (see "Provenance") |

## Options compared

| Option | Covers T2-T4 | Destination decrypts with | Depends on scan accuracy | New dependency | Verdict |
|--------|--------------|---------------------------|--------------------------|----------------|---------|
| A. Plaintext, tighter modes and retention | no | n/a | no | no | already done by 062; stays the default; add a git-worktree warning (phase 0) |
| B. Symmetric key from the secret provider | T2/T3 on the controller only | the same key: a store credential or shared key on every host, which F10 forbids ("No store database or unlock credential belongs on the managed host") | no | no | rejected |
| C. age, per-recipient public keys, hybrid PQ only | yes | its own age identity file | no | `filippo.io/age`, `filippo.io/hpke`, `golang.org/x/crypto`; raises `golang.org/x/sys` | **recommended** |
| C'. Own container on stdlib `crypto/hpke` (Go 1.26: X-Wing, ChaCha20-Poly1305) | yes | its own key file | no | no | fallback if the dependency cost is rejected |
| D. SSH host key derived (age `ssh-ed25519` recipients) | partly (no PQ) | `/etc/ssh/ssh_host_ed25519_key` | no | age + agessh (+ edwards25519) | rejected |
| E. Sidecar: plan stays plaintext, only sensitive payloads sealed | only for payloads the scan found | its own key | **yes** | age or C' | rejected |

**A (tighter plaintext).** 062 already has the strongest filesystem rule
that stays usable (owner-checked directory, `0600`, symlink-safe writes). It
cannot help once a copy leaves the directory (T2), and the default `-o .`
puts the plan into the recipe checkout, which is exactly what backups sync
and `git add -A` picks up. One cheap addition is worthwhile regardless:
warn when `dir` is inside a git worktree and `plan.jsonl` is not ignored
(phase 0).

**B (provider key).** Sealing would need a secret on the controller, and
every artifact shares one key: one leak opens all of them, and rotation
re-keys everything. A destination could only decrypt with that key on disk,
a store credential in disguise. Its one advantage (no key files) is also
available for option C by resolving the operator's age identity through the
provider (optional, phase 3).

**C (age).** age is a small, specified, audited format by a Go cryptography
maintainer: hybrid ML-KEM-768 + X25519 recipients (`age-keygen -pq`, age
>= 1.3; Fedora ships 1.3.1), multiple recipients per file, a random
per-file key, a header MAC binding the recipient list, and a 64 KiB-segment
ChaCha20-Poly1305 stream that detects truncation. gonf implements no
cryptographic format and needs no key-generation command, and an operator
can always decrypt without gonf (`age -d -i key plan.age | gonf apply -`),
including with hardware identities through age plugins, which the gonf
library path does not support.

The dependency cost is real and must be accepted explicitly: `go.mod`
(today `miekg/dns`, `x/sync`, `x/sys`, `x/tools`) gains `filippo.io/age`,
`filippo.io/hpke` and `golang.org/x/crypto`, and age's requirements raise
`golang.org/x/sys` from v0.43.0 to v0.47.0. Through minimal version
selection that raise reaches every consumer that builds against the new
gonf (the conf and dotfiles clients' pins), so their `go.sum` changes when
they bump gonf. All are pure Go and BSD-licensed, and cross-compile for
every target gonf pushes to.

**Recipient policy: `age1pq` only.** age 1.3 refuses to encrypt one file to
both hybrid and classic X25519 recipients ("incompatible recipients"),
because a classic recipient would undo the post-quantum protection of all
the others. gonf therefore accepts only hybrid recipients, so the operator
and every destination use the same type and a mix cannot arise. An `age1…`
X25519 recipient, an `ssh-…` recipient and a plugin recipient are refused
when recipients are loaded, naming the offending line (never key material)
and telling the operator to generate a key with `age-keygen -pq`. Identities
loaded by gonf are hybrid identities only, for the same reason; the age
CLI emergency path stays free to use anything.

**C' (stdlib HPKE).** Go 1.26's `crypto/hpke` offers `MLKEM768X25519()`
(X-Wing), `HKDFSHA256()` and `ChaCha20Poly1305()` with no new dependency.
HPKE seals single messages; gonf would still have to design the
multi-recipient header, the header MAC and the segmented stream, i.e.
re-derive age, and own its bugs, and it would lose the `age -d` emergency
path. Not recommended unless the dependency cost above is rejected; the
container boundary (below) keeps the choice replaceable.

**D (SSH host keys).** Attractive because every destination already has a
key, but: reading `/etc/ssh/ssh_host_ed25519_key` needs root; it reuses an
authentication key for encryption (Ed25519-to-X25519 conversion);
host-key rotation silently orphans artifacts; RSA- or ECDSA-only hosts get
no or a different path; `known_hosts` is often hashed and is not a
trustworthy recipient list; and there is no post-quantum variant, which the
policy above requires. Rejected.

**E (sidecar).** The appeal is a reviewable, diffable `plan.jsonl` with
sensitive payloads replaced by references into `plan.secrets.age`. But it is
only as good as the 062 scan: every documented scan limit leaves plaintext
secret material in the "safe" file, which is worse than an honest plaintext
plan because it claims more than it gives. It also needs a new schema
version that rewrites every payload field of every op kind as a reference,
plus a merge step on the destination. Reviewability already exists without
it: `gonf plan -redacted` (062) is the human preview; the sealed file is
the executable artifact. Rejected.

## Recommended design

### Artifact

- The sealed artifact is **age(GONF-PUSH/1 frame)**. The frame is the one
  `plan.EncodePush` already writes for push: magic, optional gzip+tar of the
  blobs, gzip of the plan JSONL. The plan inside keeps its own schema (v21,
  or v22 when sensitive); sealing adds no plan schema version.
- `gonf plan -o dir -seal` writes `dir/plan.age` `0600` with the existing
  `plan.WritePrivateFile` rules. A sealed write creates **no** `plan.jsonl`
  and **no** `blobs/`: recording goes to a `plan.MemoryStore` (as
  `planToStdout` and push already do), so no plaintext blob ever lands in
  `dir`.
- `gonf plan -seal -stdout` writes the same sealed bytes to stdout (binary
  age, no armor). A sealed stream is as safe on a pipe or in a CI log store
  as in a file, and it is the natural form for `gonf plan -seal -stdout |
  ssh host gonf apply -identity … -`. `-seal` with `-redacted` or
  `-with-secrets` is a usage error (exit 2): a preview is never sealed, and
  a sealed export needs no plaintext override.
- One package owns the format, e.g. `plan/seal` (`Seal(w, recipients)
  io.WriteCloser`, `Open(r, identities) (io.Reader, error)`,
  `ParseRecipients`, `LoadIdentities`), so CLI and api code never import age
  directly and option C' could replace it without touching them.

### Keys

| Key | Where | Made with |
|-----|-------|-----------|
| Operator identity | controller, `-identity file` (default for a non-root user: `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/identity`) | `age-keygen -pq -o …` |
| Operator recipients | `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients` by default (one `age1pq…` per line, `#` comments), overridable with `-recipients-file`, unioned with `-recipient` flags unless `-no-default-recipients` is given | `age-keygen -y` |
| Destination identity (phase 2) | on the host, e.g. `/etc/gonf/identity` (root, `0600`) | generated **on the host** by the operator; the private key never leaves it |
| Destination recipient (phase 2) | inventory: `Host(…, WithPlanRecipient("age1pq…"))` | `age-keygen -y` output copied from the host |

Rules:

- **No default identity for root.** When the effective uid is 0, `gonf
  apply` of a sealed input requires an explicit `-identity`: under
  `sudo`/`doas`, whether `HOME` and `XDG_CONFIG_HOME` are the invoking
  user's or root's depends on `env_reset`, `always_set_home` and the doas
  `keepenv`/`setenv` rules, so a default would silently pick an unexpected
  file or none. The refusal says to pass `-identity`.
- An identity file must be a regular file, not a symlink, owned by the
  effective uid, with no group or other permission bits (the `ssh` rule);
  it is opened with the same no-follow walk as secret files
  (`internal/safepath`). Errors name the path and the class, never key
  material.
- **The recipients file's trust role: it decides who can decrypt every plan
  this account ever seals.** Its content is public keys, so reading it is
  harmless, but *writing* it is not: whoever can write this file, or plant
  a symlink at its path, silently becomes a permanent recipient of every
  future `gonf plan -seal` from this account — and the only visible signal
  used to be a bare recipient count, which nobody realistically checks
  against an expected value (task `ce2` found and closed this as a real,
  probed vulnerability: a symlinked or mode-666 default recipients file was
  silently accepted and unioned in). It is therefore opened with the same
  `internal/safepath` no-follow walk as the identity file and must be a
  regular file owned by the effective uid, **refused** (not merely warned
  about) when it is writable by group or other — narrower than the identity
  file's "no group or other bit at all" rule, since a recipients file's
  read bits are not a secrecy problem the way an identity file's are.
  `gonf plan -seal` also prints the actual resolved recipient public keys
  in its output, not only a count, so an operator reviewing it has a real
  chance of noticing an unexpected one. `-no-default-recipients` opts out
  of the ambient default file entirely (sealing only to `-recipient`
  flags), and `-recipients-file <path>` names a specific, deliberately
  chosen file instead of the ambient default; a missing file named this way
  is an error, unlike a missing ambient default (which is fine — an
  operator who seals only with `-recipient` flags need never create one).
- Only `age1pq` recipients and hybrid identities (see "Recipient policy").
- A sealed write with zero recipients is refused, never degraded to
  plaintext.
- The operator's own recipient is always included when the operator
  recipients file exists, so an operator can open what they sealed.

**No forward secrecy; rotate.** A recipient key is long-lived and age has no
forward secrecy: whoever later obtains a destination's or the operator's
identity can decrypt every artifact ever sealed to it that still exists
anywhere (a backup, a CI artifact store). Two rules bound that:
artifacts are deleted once applied (retention unchanged from 062), and keys
are **rotated periodically**, recommended at least yearly and whenever a
host is rebuilt or changes hands, and immediately on suspected compromise.
Plans are cheap to regenerate, so there is no re-wrap tool: rotate by
generating a new key pair, replacing the recipient line (inventory or
recipients file) and re-recording. Keep the old identity only as long as
artifacts sealed to it must still be applied, then delete it. Removing a
recipient revokes nothing already written: delete or re-seal those
artifacts.

### Provenance

A `plan.age` that decrypts was made by someone who knew a recipient's
public key, which is everyone. gonf therefore treats successful decryption
as **confidentiality only, never provenance**:

- `gonf apply` output for a sealed input says `decrypted and applied
  plan.age (N ops)`, never "verified", "authenticated" or "trusted", and the
  docs repeat that the operator vouches for the file by choosing to apply
  it, exactly as for a plaintext `plan.jsonl` today.
- age's AEAD is not a selling point here: it only guarantees that the bytes
  are the ones the (anonymous) sealer produced. Corruption of a file in
  transit or storage is detected; substitution by a freshly sealed file is
  not.
- **Gate: no unattended sealed apply.** Nothing in gonf may apply a sealed
  plan without an operator choosing the file: no timer, cron job, watcher,
  pull agent or CI step that picks up `plan.age` from shared storage and
  applies it, and no gonf-provided recipe for one. That stays blocked until
  a signing design exists and is implemented (task `7b2`: e.g. a
  stdlib `crypto/ed25519` signer key pinned per destination, verified before
  decryption).

### Operator UX

```text
# seal for the operator (controller-local later apply, CI artifact kept for review)
gonf plan -o out -seal frontends_nsd
gonf apply -identity ~/.config/gonf/identity out/plan.age

# seal per destination (phase 2): one artifact per host, each holding only that host's plan
gonf plan -o out -seal -for frontends frontends_nsd      # out/plan-blowfish.age, out/plan-fishfinger.age
scp out/plan-blowfish.age rex@blowfish:
ssh rex@blowfish doas gonf apply -identity /etc/gonf/identity plan-blowfish.age

# pipe form
gonf plan -seal -stdout frontends_goprecords | ssh rex@fishfinger doas gonf apply -identity /etc/gonf/identity -

# emergency path without gonf's decryption (any age identity, plugins included)
age -d -i key.txt out/plan.age | gonf apply -
```

| Command | Behaviour |
|---------|-----------|
| `gonf plan -o dir -seal [-recipient r]…` | records into memory, encodes the push frame, seals to the union of `-recipient` and the operator recipients file; writes `dir/plan.age`; prints `wrote dir/plan.age (N ops, M recipients)`. Warns when `dir` still holds a `plan.jsonl` or `blobs/` from an earlier plaintext run (never deletes it: the operator's file). |
| `gonf plan -seal -stdout …` | the same sealed bytes on stdout. |
| `gonf plan -o dir -seal -for host\|cluster\|fleet …` (phase 2) | **records once per host**, with that host alone as the host selection (`recordPlanForHosts([]string{host}, …)` in `api/cluster_hosts.go`, the selection push uses), so a `ForHosts` body for another host is not in the artifact; seals each host's plan to that host's recipient plus the operator's; writes `dir/plan-<host>.age` per host. A host without `WithPlanRecipient` refuses the whole command before anything is written. `-for` with `-stdout` is refused unless it resolves to exactly one host. A plain `gonf plan` without `-for` records every `ForHosts` member (no selection), which is why a multi-host sealed artifact is never produced. |
| `gonf plan -o dir` (no `-seal`) | unchanged (062 behaviour, plaintext + warning); the warning gains a hint `use -seal`. Whether `-seal` becomes the default for sensitive plans when a recipients file exists is a separate, user-approved decision (phase 3). |
| `gonf apply [-identity f]… file` | sniffs the first line: `age-encryption.org/v1` means sealed, anything else is the existing JSONL path. The whole decrypted frame is decoded before anything is applied; the plan is then applied exactly like a plaintext plan file (`api.ApplyPlan`). |
| `gonf apply [-identity f] -` | the same sniff on stdin (a sealed stream, a GONF-PUSH/1 frame, or bare JSONL). |
| `push`, `cluster`, `fleet`, `-preview` | unchanged: plans stay in memory and on SSH. |

**Privilege: single process, as plain file apply.** `gonf apply <file>`
today calls `api.ApplyPlan` → `plan.Apply` directly (`api/plan.go`,
`plan/apply.go`); only `api.Run` (`api/task.go`) splits privilege chunks and
re-executes elevated ones through `ApplyChunksContext`. A sealed apply keeps
the file-apply semantics: it applies every op in the invoking process and
privilege, ignoring `elevate` like a plaintext file apply does, so a plan
with `Privileged()` tasks is applied with `sudo`/`doas gonf apply -identity
… plan.age`. Routing sealed apply through `ApplyChunksContext` (passing the
run dir, not `payload.PlanDir`, which is empty for a plan without blobs)
was considered and rejected for v1: it would make sealed and plaintext file
apply behave differently, and it would need the elevated child to receive a
plaintext chunk file (T8) that the single-process path never writes. If
file apply ever gains a privilege split, both paths get it together.

Failure handling: the decrypted stream is read **to EOF before any op is
applied** (age authenticates the final segment only at EOF; `DecodePush`
already reads the whole plan section, which the implementation must keep and
test with a truncated and a bit-flipped file). No matching identity, a
refused recipient type, a truncated or corrupted file: error, exit 1,
nothing applied, run dir removed. Messages name the file and the number of
recipients, never key material or plaintext.

### Plaintext after decryption

- A sealed plan **without blobs** is decoded entirely in memory
  (`DecodePush` with no plan dir): no plaintext file exists on the
  destination at any point except what the ops themselves write. This is
  the common case (every conf secret today is inline content under 512 KiB).
- A sealed plan **with blobs** extracts them into a fresh run dir
  (`plan.NewApplyRunDir`, `$TMPDIR/gonf-apply/<uid>/run-*`, `0700`), removed
  when the apply returns or fails. SIGKILL, a crash or power loss leaves it
  behind, and the existing sweep is lazy: only a later `NewApplyRunDir` of
  the same uid removes run dirs older than 24 h. For a root apply that means
  plaintext under root's `$TMPDIR/gonf-apply/0` until a root apply at least
  24 h later. Mitigation in phase 1: a sealed apply creates its run dir with
  a distinct prefix (`sealed-run-*`) and sweeps every such leftover of its
  uid whose recorded owning PID is no longer alive, regardless of age,
  before it decrypts; everything else keeps the 24 h rule. Residual risk
  (a leftover until the next sealed apply of that uid) is documented, not
  hidden.
- Go memory holding the decrypted plan is not zeroed (secrets.md).

### Schema, versioning and remote skew

- Plan schema: unchanged; no bump. The inner plan keeps v21/v22 and the
  existing header gate.
- Container version: the age header (`age-encryption.org/v1`) plus the
  inner `GONF-PUSH/1` magic. A future change to what is sealed changes the
  inner magic, not age.
- Capability probe: `gonf -sealed-version` prints `1` (like
  `-strict-preview-version`), so scripts and the phase-2 runbook can check
  a destination before shipping a sealed file.
- Older gonf given a sealed input refuses it before any op:
  `gonf apply -` fails in `plan.DecodePush` with `plan push: bad magic
  "age-encryption.org/v1"`; `gonf apply plan.age` fails decoding the first
  line as JSON in `plan.DecodePlanBytes`. There is no degrade path: gonf
  never falls back to writing or applying plaintext when sealing or
  opening fails.
- Push/preview skew: none in phases 1-3 (no wire change). Phase 4 changes the
  chunk stream and is covered by the existing push rule (a remote with an
  older release is upgraded by `EnsureRemoteGonf`; strict preview never sees
  blobs).

### Performance

Sealing adds, per recipient, one hybrid KEM encapsulation (tens of
microseconds) and about **1.46 KiB of header** (measured for an `age1pq`
recipient); the payload costs one ChaCha20-Poly1305 pass (well above 1 GB/s
on current amd64) plus 16 bytes per 64 KiB. gzip, which the push frame
already runs, dominates by an order of magnitude. Memory matches push: the
frame is built from a `MemoryStore`, so a plan with large synced trees is
held in memory on both sides, as a push already is. Per-host sealing
(phase 2) records the plan once per host, like a push to each host would.
The implementation task adds a benchmark over a representative conf plan
(`frontends`/`garage_config`) rather than relying on these estimates.

## Out of scope

- **Encrypting push, cluster or fleet transport.** SSH already provides
  confidentiality in transit; the destination needs plaintext to apply, so a
  second layer would put the key next to the data.
- **Provenance / authenticity** and anything that depends on it, above all
  unattended apply of sealed plans (see "Provenance" and task `7b2`).
- A privilege split for file apply (sealed or not).
- Secret material written by ops on the destination (files, crontab lines),
  argv in the destination's process list, package-manager output, memory
  zeroization (Go gives no guarantee; gonf does not claim it). All
  unchanged from secrets.md.
- Automatic key distribution: push does not generate, fetch or install
  identities. Creating a destination key is an explicit operator action with
  its own authority (F10: no secret credentials placed on hosts implicitly).
- Classic X25519, SSH and plugin recipients inside gonf; re-wrapping
  existing artifacts; ASCII armor; passphrase (scrypt) recipients: the age
  CLI covers the rare case.
- Sidecar or per-op encryption (option E) and schema changes for it.

## Phased implementation (follow-up tasks)

Every task depends on this design being accepted (w82) and on 062 being
merged; none may start without the user's explicit approval. Each keeps the
062 client gate: existing gonf tests, and the conf and dotfiles clients
building, listing and recording plans byte-identically unless `-seal` is
passed (a client's `go.sum` changing because of the `x/sys` raise, once it
bumps gonf, is expected and noted, not a failure).

| Phase | Task | Content |
|-------|------|---------|
| 0 | `0b2` | Warn when `gonf plan -o dir` writes a plaintext secret-bearing plan into a git worktree where `plan.jsonl` is not ignored. No crypto. |
| 1 | `1b2` | `plan/seal` package over `filippo.io/age`: `age1pq`-only recipient policy and hybrid-only identities, identity loading with owner/mode/no-follow checks, `Seal`/`Open`; tests for round trip, multi-recipient, wrong identity, mixed/classic/ssh recipients refused, and **corruption only** (truncation, bit flip; no authenticity claim). Adds the dependencies and the `x/sys` raise. |
| 1 | `2b2` | `gonf plan -o dir -seal [-recipient]…` and `-seal -stdout`; writes only `plan.age` from a `MemoryStore` push frame. |
| 1 | `3b2` | `gonf apply [-identity]… <plan.age\|->`: magic sniff, root requires `-identity`, read to EOF before apply, in-memory decode without blobs, `sealed-run-*` run dir with dead-owner sweep, single-process apply via `api.ApplyPlan`, "decrypted" wording, `gonf -sealed-version`. |
| 2 | `4b2` | Destination recipients: `WithPlanRecipient` on `Host`; `-for host\|cluster\|fleet` records once per host with that host's selection and writes `plan-<host>.age` per host. |
| 3 | `5b2` | Optional, needs a user decision: `-seal` default for sensitive plans when an operator recipients file exists, and/or the operator identity through the secret provider. |
| 4 | `6b2` | Optional: seal a multi-chunk push's sticky-dir blobs to an ephemeral per-push key sent only on each chunk's stdin, lifting 062's refusal of sensitive blobs in elevated chunks. |
| - | `7b2` | Design (not implement) signed plan artifacts; until it is implemented, unattended sealed apply stays blocked. |

Dependencies: `2b2`, `3b2` and `6b2` need `1b2`; `4b2` and `5b2` need `2b2`
and `3b2`; `0b2` needs only 062; `7b2` needs only w82.
