# Sealed plan artifacts (design, task w82)

Status: **phases 0-2 implemented** (tasks `0b2`, `1b2`, `2b2`, `3b2`, `4b2`):
`gonf plan -seal` (whole-plan and per-destination `-for`) and `gonf apply
-identity` both exist. Phases 3+ (`5b2` optional default flip / provider
identity, `6b2` optional sealed sticky-dir blobs, `7b2` signing design) are
still design-only follow-ups; the "Phased implementation" table at the end
of this document tracks exact status per task.

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
| `gonf plan -o dir -seal [-recipient r]…` | records into memory, encodes the push frame, seals to the union of `-recipient` and the operator recipients file; writes `dir/plan.age`; prints `wrote dir/plan.age (N ops, M recipients)`, then one `  recipient age1pq1…<last 8> sha256:<16 hex>` line per recipient (the full public key instead under the top-level `-verbose`). Warns when `dir` still holds a `plan.jsonl` or `blobs/` from an earlier plaintext run (never deletes it: the operator's file). |
| `gonf plan -seal -stdout …` | the same sealed bytes on stdout. |
| `gonf plan -o dir -seal -for host\|cluster\|fleet …` (task 4b2) | **records once per host** (`api.RecordPlanForHost`, `internal/cli/plan_seal_for.go`), with the same push-alias host selection `PushHost` would use for that host (`inventory.SelectionForHosts([]string{host})`), so a `ForHosts` body for a host **outside that substring-based selection** is not in the artifact; every host's plan is recorded, sealed and staged (a hidden, sealed `0600` staging file per host, only one host's frame in memory at a time, task `qg2`) before any `plan-<host>.age` is published, so a failure partway through leaves nothing written. This is a superset, not an exact single-host match (same caveat as a plain `gonf push`, see "Runbook" below): a host whose name or SSHHost is a substring of the target's (or vice versa) IS in the selection, and its `ForHosts` body — and any secret it reads — can physically land in the target's artifact. Seals each host's plan to that host's `api.WithPlanRecipient` plus the union of `-recipient`/recipients-file; writes `dir/plan-<host>.age` per host (the host name sanitized to `[A-Za-z0-9._-]`). Refuses before writing anything when any resolved host lacks a recipient (naming it), when the `-recipient`/recipients-file union is empty (same zero-recipient refusal `-seal` alone has, task `mg2`: sealing to the destination host's recipient only would produce an artifact the operator who just ran the command cannot open), or when two resolved hosts would sanitize to the same filename. `-for` requires `-seal` (a static usage error otherwise) and, with `-stdout`, is refused unless it resolves to exactly one host. A plain `gonf plan` without `-for` records every `ForHosts` member (no selection), which is why a multi-host sealed artifact is never produced. |
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

**Memory bound (task be2).** Reading the whole decrypted stream before
applying anything is required (above), but doing it with a plain,
unbounded `io.ReadAll` is itself a hazard T10 makes concrete: since
recipients are public, ANYONE can produce a `plan.age` that decrypts
(never one that is trusted — see "Provenance" — but decryption alone is
enough to reach the code below). A `plan.age` whose plan section is a
high-ratio gzip stream ("gzip bomb") drove peak RSS to **~10.5 GB from a
3.0 MB file** before this task's fix, which an attacker could plant
anywhere T2 already worries about (a backup, a CI artifact store, a
shared directory) for a clean, silent OOM on the next `gonf apply`. Two
independent caps close this, both named constants rather than inline
numbers so they are easy to find and raise if a legitimate plan ever
needs more:
- `maxSealedFrameBytes` (`internal/cli/cli.go`, 512 MiB) bounds the
  fully-decrypted GONF-PUSH/1 frame `decryptAndDecodeSealedPush` reads
  from `seal.Open`'s reader — the read the paragraph above requires can
  still happen, but only up to this many bytes before it refuses loudly.
- `plan.MaxDecompressedPushPlan` (`plan/pushwire.go`, 64 MiB by default)
  separately bounds the plan section's OWN gzip decompression inside that
  frame (`readGzipOrRaw`/`maybeGunzip`), which is where the 3 MB-to-10.5 GB
  amplification actually happens — this also fixes the pre-existing
  `gonf apply -` (stdin) case, which reached the same unbounded
  decompression even before sealed apply (task 3b2) existed.

Either cap refuses with a named sentinel error (`errSealedFrameTooLarge`,
`plan.ErrPushPlanTooLarge`) and applies nothing, exactly like the other
failures in this section — this is availability protection only, not a
new trust boundary: a plan under both caps is exactly as untrusted as
before (see "Provenance").

**Amplification WITHIN the cap (task 3g2).** be2's own 256 MiB figure was
still 4x more than this section's "tens of MB" legitimate envelope, and
`io.ReadAll`'s internal doubling-growth buffer meant the moment its
post-hoc length check ran, it could already have over-allocated close to
2x the bytes it actually needed — so a stream ENTIRELY inside the accepted
cap (no refusal at all) could still peak at roughly `2 * cap` in memory.
Measured on the pre-3g2 code: a 254,845-byte compressed input decompressing
to 250 MiB peaked at 855 MB RSS (~3,360x the compressed size), and a
203,883-byte input decompressing to 200 MiB peaked at 752–961 MB
(~3,700–4,700x) — both comfortably under the old 256 MiB cap, so nothing
refused either one, and gonf's own fleet includes 1 GB-class SBCs this
could genuinely OOM. Task 3g2 lowered `plan.MaxDecompressedPushPlan` to 64
MiB (an exported `var`, raisable by an embedder with an unusually large
legitimate plan) and replaced `maybeGunzip`'s `io.ReadAll` with `readCapped`,
which reads in small fixed-size chunks and refuses the moment the running
total would exceed the cap — so even a stream that is ultimately refused
never grows the buffer anywhere near the cap, let alone past it.

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
(`-for`, task 4b2) records the plan once per host, like a push to each host
would, and like a push it holds only one host's plan and sealed frame at a
time (task qg2): each host's frame is staged to a hidden `0600` file in the
already-verified output directory and released before the next host is
recorded, and every staged file is renamed into place as `plan-<host>.age`
only after all hosts succeeded. Peak memory is therefore one host's
compressed plan, not N of them, while a failure while recording, sealing or
staging any host still leaves nothing written (the staged files are
removed). Only the final renames can fail partway (a pre-existing directory
named `plan-<host>.age`, say); that refusal names the artifacts already
written and those not written. A `gonf plan` killed between staging and
renaming can leave a hidden `.plan-<host>.age.staging-<pid>` file behind; it
holds the same sealed bytes the artifact would, never plaintext.
The implementation task adds a benchmark over a representative conf plan
(`frontends`/`garage_config`) rather than relying on these estimates.

### Runbook: host keys and shipped plan.age (task 4b2)

This is the concrete, step-by-step version of the "seal per destination"
row of "Operator UX" above, for an operator setting up `-for` against a real
host for the first time.

**1. Generate the destination's identity, on the destination, as the user
that will apply the plan (root for a `Privileged()` recipe):**

```text
ssh rex@blowfish
sudo age-keygen -pq -o /etc/gonf/identity
chmod 600 /etc/gonf/identity          # age-keygen already sets this; verify it
```

`age-keygen -pq` prints the matching public key as `Public key:
age1pq1…`. The private key never leaves the host: this command must be run
on the destination itself, not generated on the controller and copied
over — copying a private key defeats the point of per-host recipients (an
operator who can read it can decrypt anything ever sealed to it, forever;
see "No forward secrecy; rotate" above).

**2. Add the printed public key to the inventory, on the controller:**

```go
api.Host("blowfish",
    api.WithSSHHost("blowfish.example"),
    api.WithPlanRecipient("age1pq1…"), // from step 1's "Public key:" line
)
```

`WithPlanRecipient` validates the value at registration (the same
`age1pq`-only, hybrid-only policy every recipient in this design follows);
a malformed or non-hybrid value is refused immediately as a declaration
error, naming the host, never silently accepted.

**Naming hosts so an artifact stays exactly one host's (task ng2):** `-for`'s
per-host recording uses `inventory.SelectionForHosts`, the same
substring-based selection a plain `gonf push` relies on (see "Operator UX"'s
`-for` row and `internal/inventory/destination.go`) — it is a superset, not
an exact single-host match. If `blowfish`'s inventory name or SSHHost is a
substring of another registered host's (or vice versa — e.g. `web` and
`web01`, or a host whose SSHHost is `web.db.example` next to an unrelated
host named `db`), that other host's `ForHosts` body, including any secret it
reads, is recorded alongside `blowfish`'s and physically ends up inside
`plan-blowfish.age`, even though a `when_begin`/`hostname_contains` guard
keeps it from ever actually applying there. Where an artifact must contain
exactly one host's secrets — e.g. it will be shipped to a different admin,
team or CI system than the one that controls the other host — avoid
registering host names or SSH hosts that are substrings of one another, or
audit `inventory.SelectionForHosts([]string{"blowfish"})`'s result before
shipping.

**3. Seal and ship the per-host artifact.** `-for` also seals to your OWN
base recipients — the union of `-recipient` flags and your recipients file
(`${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients`, "Keys" above) — the
same as plain `-seal`, and refuses up front if that union is empty, since a
`plan-blowfish.age` sealed to blowfish's recipient alone is not one you
could open again yourself to review or re-apply. If you have not already,
create the recipients file first (one `age1pq…` recipient per line, your own
key from `age-keygen -pq`) or pass `-recipient age1pq…` explicitly:

```text
gonf plan -o out -seal -for blowfish frontends_nsd   # -> out/plan-blowfish.age
scp out/plan-blowfish.age rex@blowfish:
```

(`-for` also takes a cluster or fleet name — see "Operator UX" — to ship
one artifact per member host in one command.)

**4. Apply on the destination, as the operator, with the identity from
step 1:**

```text
ssh rex@blowfish
sudo gonf apply -identity /etc/gonf/identity plan-blowfish.age
rm plan-blowfish.age                  # delete once applied; see "Retention"
```

`gonf apply` here prints `decrypted and applied plan-blowfish.age (N ops)`
— confidentiality only, never a claim of authenticity (see "Provenance"):
nothing about this step is, or should be, automated or unattended (no
timer, cron job or pull agent) until task `7b2`'s signing design exists.

**5. Rotate the key periodically:** at least yearly, whenever the host is
rebuilt or changes hands, and immediately on suspected compromise (see "No
forward secrecy; rotate"). Rotation is: repeat step 1 to overwrite
`/etc/gonf/identity` with a fresh pair, update `WithPlanRecipient` in the
inventory to the new public key, and re-seal — there is no re-wrap tool,
and an artifact already sealed to the old key still needs the old identity
to open (keep it only as long as such an artifact must still be applied,
then delete it).

## Phase 4 design: sealed multi-chunk sticky-dir blobs (task `6b2`)

Status: **design only, expanded here by task `6b2`; not implemented.** Task
`6b2` (this design's own phase-4 entry) read this section's earlier
one-line table summary, task `062`'s refusal it would lift, and the
sticky-dir/chunk-stdin code it would touch, in full, and decided — because
of the phase's "optional" framing, its genuine complexity, and this
codebase's established rigor for privileged-apply/secret-handling changes
(`062` alone took eight review rounds for a narrower change with no new
crypto or wire version) — that landing an untested-by-review, one-pass
implementation of this in a single session was the wrong call. This section
is the thorough design the task's own scope-discipline guidance asked for
instead, concrete enough that the follow-up tasks below do not need to
re-derive it.

### What this closes

`062` added `internal/remote/delivery.go`'s `refuseSensitiveStickyBlobs`:
`Delivery.ToHost` refuses, before any SSH traffic, a multi-chunk plan whose
elevated chunk carries a sensitive op with blob content
(`plan.SensitiveElevatedBlobs`), because that blob would be staged
plaintext into `/tmp/gonf-apply-sticky-<plan>-<host>` — a directory created
and owned by the SSH **login** user (`uploadSticky`/`pushBlobs` in
`internal/remote/remote.go`, always unprivileged, "even when the first
chunk is elevated" per `ToHost`'s own doc comment) — before the elevated
chunk (root, via sudo/doas) ever reads it. Today the operator's only way
around the refusal is to keep such content under `plan.MaxInlineContent` (a
single-chunk plan embeds blobs in its own frame, extracted by the elevated
apply itself) or push the privileged task alone. This phase seals the
sticky-dir blob instead of forbidding it, so the login user's directory
never holds plaintext secret content.

### Key insight: reuse phase-1/3's "decrypt into a private run dir" pattern, not a new decrypt-on-read path

An early read of this design looked for the smallest change and considered
making `plan.ReadFile` (`plan/blob.go`, the single-file blob reader
`resource/file/planwire.go`'s `fileContent` calls) and
`resource/dir/planwire.go`'s `syncDirBlobTree` (the **tree**-blob reader a
sensitive `sync_dir` op also reaches — `plan.SensitiveElevatedBlobs` checks
`op.Blob != ""` for any op kind, not only `file`) each transparently detect
and decrypt an age-sealed blob in place. That would touch two independent
resource packages' read paths (and every future blob-consuming kind after
them), duplicate the sealed-file detection logic, and leave the tree case
awkward: a tree blob is many entries (`plan.BlobEntry`: file/dir/symlink)
under one ref, and sealing only the file entries' content while leaving
directory/symlink structure exposed under the sticky dir is a second,
narrower design of its own.

**Recommended instead:** seal each blob **ref** (a whole `file` blob, or a
whole `sync_dir` tree ref, exactly the unit `writeTreeTar`/`writeBlobsGzipTar`
already emit as one tar member) as one opaque age stream per ref, uploaded
into the sticky dir as sealed bytes rather than as tar entries a resource
package walks directly. The destination's elevated chunk, once it holds the
ephemeral identity (below), decrypts every sealed ref its own ops need into
a **fresh, private, root-owned run directory** — reusing
`plan.NewSealedApplyRunDir()` (`plan/staging.go`, already built and
reviewed for phase-1/3's own "sealed plan with blobs" case: `0700`,
`sealed-run-<pid>-*` naming, immediate sweep of dead-PID leftovers instead
of the lazy 24h rule) — and points that chunk's `plan.ApplyContext.PlanDir`
at the private directory instead of the sticky one for the ops that need
it, before `plan.ApplyWithContext` runs. **`resource/file` and
`resource/dir` need no change at all**: they keep reading an ordinary
plaintext blob from whatever `PlanDir` they are given, exactly as today.
This also reuses an already-reviewed sweep/lifecycle mechanism instead of
inventing a second one, and keeps the blast radius close to the task's own
`6b2` Scope annotation (`internal/remote/delivery.go`,
`plan/sensitive.go`), plus the two files that pattern requires touching on
top of it: `plan/pushwire.go` (wire extension) and `internal/cli/cli.go`
(the destination-side decrypt-before-apply step, which phase-1/3's own
sealed-apply already lives in).

### Wire extension: no conflict with the cancel-pipe protocol

The task brief flagged the existing stdin cancel-byte protocol
(`internal/applyproto.CancelPipeFlag`/`CancelByte`, tasks 6d2/xd2) as a
coexistence risk to check. Reading `api/apply_chunks.go` end to end: that
protocol belongs **only** to the **local** elevated sudo/doas re-exec
(`runElevatedCmd`/`wireElevatedCancelPipe`), used when a task with
`Privileged()` ops is applied locally (`api.Run` → `ApplyChunksContext`).
There the child's stdin is dedicated to the cancel pipe and the plan is
passed by **path** (`chunk-elevated.jsonl`), never by stdin. The **remote**
multi-chunk push path (`internal/remote`, this phase's actual scope) is a
different flag entirely (`applyproto.RelayedFlag`) and a different stdin
use: each chunk's stdin is the `GONF-PUSH/1` frame itself
(`plan.EncodePush`/`streamChunks`), with no cancel-byte multiplexing on it
at all. **The two protocols never share a process or a stdin stream, so
there is nothing to multiplex or conflict with** — the wire extension below
is free to extend the `GONF-PUSH/1` frame however it needs to, independent
of `internal/applyproto`.

The frame itself already reserves room for this: after the `"blobs 0\n"` /
`"blobs 1\n"` line and before the `"plan\n"` marker (`EncodePush`/
`DecodePush` in `plan/pushwire.go`) is a natural, backward-extensible slot
for one optional line, e.g. `"key <base64 age1pq ephemeral identity>\n"`,
present only on a chunk that needs to decrypt a sealed sticky ref. Whether
to spell this as a new line inside `GONF-PUSH/1` (simplest: `DecodePush`
already reads line by line and an absent line is just skipped by an older
reader that does not know to look for it, **except an older reader does not
skip an unrecognized line — it would fail parsing `"plan\n"` where it found
`"key ...\n"` instead**, so this cannot be silently forward-compatible) or a
new `GONF-PUSH/2` magic (clean version bump; `DecodePush`'s existing
`pushMagic` string-equality check already refuses anything but exactly
`"GONF-PUSH/1"`, so a `/2` frame is refused outright by every gonf that
predates this phase, with no silent misparse) is the first concrete
decision the implementation task must make and test both directions of
(old remote receiving a new frame, new remote receiving an old one) — this
design recommends the `/2` bump, because "refuse cleanly" is a strictly
safer failure mode for a security-relevant field than "hope every reader
skips unknown lines correctly," and it composes with the capability gate
below exactly like `RequireRemoteRelayed` already does for `-relayed`.

### Key lifecycle

- **Generation.** `Delivery.ToHost` (or `uploadSticky`) generates one fresh
  hybrid identity per push via `age.GenerateHybridIdentity()`
  (`filippo.io/age`, already a dependency since `1b2`; `plan/seal` has no
  ephemeral-generation helper today and needs one —
  `seal.GenerateEphemeral() (Identity, Recipient, error)`, wrapping that
  call and building `plan/seal`'s own `Identity`/`Recipient` wrapper types
  around it, entirely in memory). Per the task's revised scope note (added
  after the `w82` design review): this key is internal to gonf, not an
  operator recipient, so the `age1pq`-only *operator* recipient policy does
  not constrain it — but it uses the same hybrid type for consistency
  unless a measured cost argues otherwise (`### Performance` above already
  measures hybrid overhead as small: ~1.46 KiB header, one KEM
  encapsulation).
- **Never touches disk.** The identity is held only in the `Delivery`/push
  call stack's memory (controller side) and the elevated chunk-apply call
  stack's memory (destination side, after being read off stdin); neither
  side ever calls `plan/seal.LoadIdentities` (the file-backed loader,
  which is for operator/destination long-lived identities, not this one) or
  writes it to a temp file. `plan/seal` needs one new encode/decode pair
  for the wire line — a single-line textual form (mirroring the
  `AGE-SECRET-KEY-PQ-1…` identity-file line format `parseIdentityLine`
  already parses) rather than reusing `LoadIdentities`, since that
  function's contract is inseparable from its file-ownership/no-follow
  checks, which make no sense for a value that arrives over stdin.
- **Never logged.** Every place this design's key line passes through
  (`streamChunks`'s buffer, `SSHRunner`'s relayed stderr, `internal/cli`'s
  arg/flag parsing) must be audited the way `062`'s `logger.Redact`/
  `RedactingWriter` already audits secret payloads — the key line is a
  **new** line the existing redactor does not know about by construction
  (it is not a plan op payload), so it needs its own explicit "never
  printed, never included in an error" review, not an assumption that the
  existing secret redaction happens to cover it.
- **Single use.** One identity per `Delivery.ToHost` call (one push to one
  host); a retried or re-run push generates a new one. No rotation
  mechanics are needed (unlike the operator/destination identities in
  phases 1-3): there is nothing to rotate, since nothing durable was ever
  sealed to it — `pushRemoveSticky` already removes the sticky dir (sealed
  bytes and all) after the last chunk, same as today.
- **Capability gate.** A new `internal/remote` capability check (e.g.
  `RequireRemoteSealedSticky`), following `RequireRemoteRelayed`'s exact
  pattern (`internal/remote/sync_gonf.go`: a fixed release floor, checked
  before `ToHost` decides to seal rather than refuse) — `EnsureRemoteGonf`
  self-heals an old remote for `push` (installs/upgrades before the sealed
  path is used), and `Preview` mode is unaffected (it already refuses any
  blob-backed plan outright, `hasBlobs && d.Mode == Preview`, before this
  code is ever reached).

### Lifting the 062 refusal

`refuseSensitiveStickyBlobs` is removed unconditionally once the sealed
path lands — this phase's whole point is that the condition it refused
(plaintext secret content in a login-user-owned directory) no longer holds,
not that sealing becomes an opt-in flag the refusal falls back to. Precisely:

- `plan.SensitiveElevatedBlobs` keeps identifying which ops need sealing
  (unchanged) — `ToHost` uses it to decide *which* blob refs to seal (only
  the ones an elevated, sensitive op needs), not merely whether to refuse.
- Every OTHER sticky ref (non-sensitive, or elevated-but-not-blob) uploads
  exactly as today, unsealed — this phase changes what happens to the
  refs `plan.SensitiveElevatedBlobs` already names, nothing else, so a
  plan with no such refs is byte-identical on the wire to today (the
  062-era client-gate invariant: "non-blob-sealing apply paths remain
  byte-identical" from this task's own gate instructions).
- `internal/remote/delivery_test.go` (or wherever `062` pinned the refusal)
  loses that refusal test and gains: a round-trip test through the real
  sticky-upload/chunk-apply path with a sensitive elevated blob (proving it
  applies correctly and that the sticky dir's on-disk bytes are NOT the
  plaintext at any point during the test), a tamper test (corrupted sealed
  ref refuses cleanly, applies nothing), a missing-key test (an elevated
  chunk whose `GONF-PUSH/2` frame lacks the key line but whose ops need a
  sealed ref refuses with a message naming the ref, never key material),
  and old-remote compatibility (a remote below the capability floor is
  upgraded by `EnsureRemoteGonf` before ever seeing a sealed ref).

### Threat-model update

Extends the table in "Threat model" above, specifically for T7/T8's sticky-
dir case:

| # | Adversary / exposure | Today (062 refuses instead) | With phase 4 |
|---|---|---|---|
| T7' | SSH login user reading the sticky dir | n/a (refused before upload) | sealed bytes only; readable plaintext only after the elevated chunk decrypts its own copy into a `0700` root-owned `sealed-run-*` dir, removed on return like phase-1/3's own sealed-apply blobs |
| T8' | Local elevated re-exec | n/a | not reached — this phase is the *remote* push path only; the local `api/apply_chunks.go` re-exec is untouched (see "no conflict" above) |

Unchanged: no forward secrecy is needed here (the key is single-use and
never durable), and this is confidentiality only, same caveat as every
other phase — a sealed sticky ref proves nothing about who staged it.

### Follow-up tasks (this phase's own sub-phases)

Mirroring how phases 0-3 were split, rather than landing all of the above
as one unreviewed change:

| Step | Content |
|------|---------|
| 4a | `plan/seal`: `GenerateEphemeral()` (in-memory hybrid identity + recipient, no disk), plus a stdin-safe single-line identity encode/parse pair distinct from the file-backed `LoadIdentities`. Small, self-contained, unit-testable without any remote/wire code. Depends on `1b2`. |
| 4b | Wire + delivery: `plan/pushwire.go`'s `GONF-PUSH/2` extension (old/new compatibility tested both directions), `internal/remote` seals only the specific `plan.SensitiveElevatedBlobs` refs an elevated chunk needs and sends the ephemeral identity only on that chunk's own stdin frame, `RequireRemoteSealedSticky` capability gate wired into `EnsureRemoteGonf`. Depends on 4a. |
| 4c | Destination staging + refusal removal + full rigor: `internal/cli` decrypts needed sealed refs into `plan.NewSealedApplyRunDir()` before the chunk applies (no `resource/file`/`resource/dir` changes); removes `refuseSensitiveStickyBlobs`; full gate suite (build/vet/gofmt/`go test -race -shuffle=on`/staticcheck/errcheck-zero/4x GOOS vet); client gate (temporary `go.work` with dotfiles + conf, confirming non-sealing paths byte-identical); revert-and-retest self-review; docs (`secrets.md`, this file, `plan.md`). Depends on 4b. |

None of these three may start without the user's explicit approval, same as
every other phase in this design.

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
| 2 | `4b2` (done) | Destination recipients: `api.WithPlanRecipient` on `Host`; `gonf plan -seal -for host\|cluster\|fleet` records once per host (`api.RecordPlanForHost`) and writes `plan-<host>.age` per host, sealed to that host's recipient plus the operator's; refuses up front when a target host lacks a recipient, when the operator's own base recipients (`-recipient`/recipients-file) are empty (task `mg2`), or when two hosts would sanitize to the same filename; `-for` with `-stdout` only when it resolves to exactly one host. See "Runbook: host keys and shipped plan.age" above. |
| 3 | `5b2` | Optional, needs a user decision: `-seal` default for sensitive plans when an operator recipients file exists, and/or the operator identity through the secret provider. |
| 4 | `6b2` | Optional: seal a multi-chunk push's sticky-dir blobs to an ephemeral per-push key sent only on each chunk's stdin, lifting 062's refusal of sensitive blobs in elevated chunks. **Scoped down to design only** (see "Phase 4 design: sealed multi-chunk sticky-dir blobs" above) rather than a one-session implementation of security-sensitive privileged-apply plumbing; split into its own sub-phases `yf2` (ephemeral seal primitive, done: `seal.GenerateEphemeral`, `seal.EncodeEphemeral`, `seal.ParseEphemeral` in `plan/seal/ephemeral.go`) → `zf2` (wire extension + delivery) → `0g2` (destination staging, refusal removal, full gates, self-review). |
| - | `7b2` | Design (not implement) signed plan artifacts; until it is implemented, unattended sealed apply stays blocked. |

Dependencies: `2b2`, `3b2` and `6b2` need `1b2`; `4b2` and `5b2` need `2b2`
and `3b2`; `0b2` needs only 062; `7b2` needs only w82. `6b2`'s own
sub-phases: `zf2` needs `yf2`, `0g2` needs `zf2`, `yf2` needs `1b2` (same as
`6b2` itself).
