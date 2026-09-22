# Sealed plan artifacts (design, task w82)

Status: **design only, not implemented.** Nothing below exists in gonf yet.
Implementation needs this design accepted and the user's explicit approval;
the follow-up tasks are listed in the last section. Task 062 (secret-aware
plans, schema v22) is the baseline: it marks secret-bearing ops `sensitive`,
keeps `plan.jsonl` plaintext under private-filesystem protections, refuses
`gonf plan -stdout` for such plans unless `-with-secrets`, and documents in
[secrets.md](secrets.md) ("Not provided") that durable encryption needs its
own design. This is that design. It answers F10 / P8 of
[consumer-dsl-simplification-plan.md](consumer-dsl-simplification-plan.md)
("If durable artifacts are required, design recipient encryption or a
protected sidecar before claiming encrypted plans").

**Summary.** `gonf plan -o dir -seal` writes one file, `dir/plan.age`: the
existing GONF-PUSH/1 frame (plan JSONL plus blobs, exactly what `push`
streams) encrypted with [age](https://age-encryption.org/v1) to a list of
public-key recipients. `gonf apply dir/plan.age -identity key` (or `-` on
stdin) decrypts it into the same private run directory a push uses. Push,
cluster and fleet do not change. Keys are ordinary age key pairs
(`age-keygen -pq`); a destination holds its own private key, never a secret
store credential. Sealing gives confidentiality at rest, not authenticity.

## What travels where today

| Path | Plaintext copies of secret material | Protection (after 062) |
|------|-------------------------------------|------------------------|
| `gonf plan -o dir` | `dir/plan.jsonl` and `dir/blobs/` until the operator deletes them; the default `dir` is `.`, normally the recipe checkout | `plan.jsonl` `0600`, `blobs/` `0700`, owner/writability-checked `dir` (plan.md "The output directory"), stderr warning naming the sensitive ops |
| `gonf plan -stdout -with-secrets` | wherever stdout goes | explicit operator export; refused without the flag |
| `gonf <task>` (local run) | private `$TMPDIR` plan directory with blobs; removed on return | `0700` dir; lost only on SIGKILL/crash |
| Local elevated re-exec (`api/apply_chunks.go` `defaultElevatedApply`) | `chunk-elevated.jsonl` written `0600` into that same private plan directory, read by the sudo/doas child | same directory as above, removed on return |
| `push`, `cluster`, `fleet` | controller memory (`plan.MemoryStore`), SSH stdin (GONF-PUSH/1, `plan/pushwire.go`), destination run dir `$TMPDIR/gonf-apply/<uid>/...` | SSH encryption in transit; owner-only run dir, wiped after apply, stale dirs swept after 24 h |
| Multi-chunk push with blobs (`internal/remote/delivery.go`) | sticky `/tmp/gonf-apply-sticky-<plan>-<host>` owned by the SSH login user | removed after success only; 062 refuses sensitive blobs in an elevated chunk before any SSH traffic |
| Destination | the managed files themselves, validation candidates | by design (the destination must write them); candidates are private and removed |

## Threat model

Assets: the secret payloads of sensitive ops (`content_b64`, blobs,
`template_data`, member contents, argv, environment), plus anything the 062
scan misses (see "Limits of the scan" in secrets.md: transformed values,
synced trees, weak secrets in identities, non-UTF-8 template strings).

| # | Adversary / exposure | Today | With sealing |
|---|----------------------|-------|--------------|
| T1 | Another local account on the controller | blocked by `0600`/`0700` and the dir rule | unchanged (still blocked) |
| T2 | **Copies of the artifact outside gonf's control**: backups and snapshots of the checkout (`-o .`), Syncthing/Dropbox, `git add -A` of a stray `plan.jsonl`, CI artifact uploads, copying a plan to a host with scp and forgetting it | plaintext, readable by whoever reads the copy | ciphertext; readable only with a recipient's private key. **This is the threat sealing exists for.** |
| T3 | Disk theft or disposal of controller / CI runner | plaintext unless the disk is encrypted | ciphertext |
| T4 | Harvest now, decrypt later (a durable backup read years later) | n/a | mitigated by the hybrid ML-KEM-768 + X25519 recipient type (`age1pq…`) |
| T5 | Network observer on push | SSH | unchanged; no second layer (below) |
| T6 | Destination staging: run dir, sticky dir | owner-only / login-user-owned, short-lived | unchanged in phases 1-3; phase 4 seals sticky blobs to an ephemeral key |
| T7 | Local elevated re-exec | private dir of the controller user, short-lived; root reads it anyway | unchanged: the parent decrypts, the child keeps reading a private plaintext chunk |
| T8 | Same-user malware on the controller, root on the destination | can read the secret store, the identity and the written files | **out of scope**; no file format helps |
| T9 | Tampering / substitution of a plan a later `gonf apply` trusts | directory protections only | AEAD detects modification, **but anyone holding a recipient's public key can produce a valid `plan.age`**; trust still comes from where the file is stored (see "Out of scope") |

## Options compared

| Option | Covers T2-T4 | Destination decrypts with | Depends on scan accuracy | New dependency | Verdict |
|--------|--------------|---------------------------|--------------------------|----------------|---------|
| A. Plaintext, tighter modes and retention | no | n/a | no | no | already done by 062; keep as the default and add a git-worktree warning (phase 0) |
| B. Symmetric key from the secret provider | T2/T3 on the controller only | the same key: a store credential or shared key on every host, which F10 forbids ("No store database or unlock credential belongs on the managed host") | no | no | rejected |
| C. age, per-recipient public keys (X25519 or hybrid PQ) | yes | its own age identity file | no | `filippo.io/age` (+ `golang.org/x/crypto`) | **recommended** |
| C'. Own container on stdlib `crypto/hpke` (Go 1.26: X-Wing, ChaCha20-Poly1305) | yes | its own key file | no | no | fallback if the dependency is rejected |
| D. SSH host key derived (age `ssh-ed25519` recipients) | yes | `/etc/ssh/ssh_host_ed25519_key` | no | age + agessh (+ edwards25519) | rejected |
| E. Sidecar: plan stays plaintext, only sensitive payloads sealed | only for payloads the scan found | its own key | **yes** | age or C' | rejected |

**A (tighter plaintext).** 062 already has the strongest filesystem rule
that stays usable (owner-checked directory, `0600`, symlink-safe writes). It
cannot help once a copy leaves the directory (T2) and the default `-o .`
puts the plan into the recipe checkout, which is exactly what backups sync
and `git add -A` picks up. One cheap addition stays worthwhile regardless:
warn when `dir` is inside a git worktree and `plan.jsonl` is not ignored
(phase 0).

**B (provider key).** Sealing would need a secret on the controller, so CI
that only records plans would need store access it does not otherwise need
for sealing, and every artifact shares one key: one leak opens all of them,
and rotation re-keys everything. A destination could only decrypt with that
key on disk, a store credential in disguise. Its one advantage (no key
files) is also available for option C by resolving the operator's age
identity through the provider (optional, phase 3).

**C (age).** age is a small, specified, audited format by a Go
cryptography maintainer: X25519 or hybrid ML-KEM-768 + X25519 recipients
(`age-keygen -pq`, age >= 1.3; Fedora ships 1.3.1), multiple recipients per
file, a random per-file key, a header MAC binding the recipient list, and a
64 KiB-segment ChaCha20-Poly1305 stream that detects truncation. Using it
means gonf implements no cryptographic format and needs no key-generation
command: `age-keygen` makes keys, and an operator can always decrypt
without gonf (`age -d -i key plan.age | gonf apply -`), including with
hardware identities through age plugins, which the gonf library path does
not need to support. Cost: two module dependencies in a deliberately small
`go.mod` (today `miekg/dns`, `x/sync`, `x/sys`, `x/tools`); both are pure
Go, BSD-licensed, and cross-compile for every target gonf pushes to.

**C' (stdlib HPKE).** Go 1.26's `crypto/hpke` offers
`MLKEM768X25519()` (X-Wing), `HKDFSHA256()` and `ChaCha20Poly1305()` with no
new dependency. HPKE seals single messages; gonf would still have to design
the multi-recipient header, the header MAC and the segmented stream, i.e.
re-derive age, and own its bugs. Not recommended while age is acceptable;
the container boundary (below) keeps the choice replaceable.

**D (SSH host keys).** Attractive because every destination already has a
key, but: reading `/etc/ssh/ssh_host_ed25519_key` needs root (an
unprivileged `gonf apply` could not decrypt); it reuses an authentication
key for encryption (cross-protocol reuse via the Ed25519-to-X25519
conversion); host-key rotation silently orphans artifacts; RSA- or
ECDSA-only hosts get no or a different path; `known_hosts` is often hashed
and is not a trustworthy recipient list; and there is no post-quantum
variant. Rejected.

**E (sidecar).** The appeal is a reviewable, diffable `plan.jsonl` with
sensitive payloads replaced by references into `plan.secrets.age`. But it is
only as good as the 062 scan: every documented scan limit (a hashed or
base64-transformed secret, a synced tree, a weak secret in an identity,
invalid UTF-8) leaves plaintext secret material in the "safe" file, which
is worse than an honest plaintext plan because it claims more than it
gives. It also needs a new schema version that rewrites every payload
field of every op kind as a reference plus a merge step on the destination.
Reviewability already exists without it: `gonf plan -redacted` (062) is the
human preview; the sealed file is the executable artifact. Rejected.

## Recommended design

### Artifact

- The sealed artifact is **age(GONF-PUSH/1 frame)**. The frame is the one
  `plan.EncodePush` already writes for push: magic, optional gzip+tar of the
  blobs, gzip of the plan JSONL. The plan inside keeps its own schema
  (v21, or v22 when sensitive); sealing adds no plan schema version.
- File name `plan.age` in `dir`, written `0600` with the existing
  `plan.WritePrivateFile` rules. A sealed write creates **no** `plan.jsonl`
  and **no** `blobs/`: recording goes to a `plan.MemoryStore` (as
  `planToStdout` and push already do), so no plaintext blob ever lands in
  `dir`.
- The age payload is binary (not ASCII-armored); armor is YAGNI.
- One package owns the format, e.g. `plan/seal` (`Seal(w, recipients)
  io.WriteCloser`, `Open(r, identities) (io.Reader, error)`,
  `ParseRecipients`, `LoadIdentities`), so CLI and api code never import age
  directly and option C' could replace it without touching them.

### Keys

| Key | Where | Made with |
|-----|-------|-----------|
| Operator identity | controller, `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/identity`, or `-identity file` | `age-keygen -pq -o …` |
| Operator recipients | `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/recipients` (one `age1…`/`age1pq…` per line, `#` comments), or `-recipient` flags | `age-keygen -y` |
| Destination identity (phase 2) | on the host, e.g. `/etc/gonf/identity` (root, `0600`) or the login user's config dir | generated **on the host** by the operator; the private key never leaves it |
| Destination recipient (phase 2) | inventory: `Host(…, WithPlanRecipient("age1pq…"))` | `age-keygen -y` output copied from the host |

Rules:

- Accepted recipient types: native X25519 (`age1…`) and hybrid
  (`age1pq…`); `ssh-ed25519`/`ssh-rsa` recipients and plugin recipients are
  refused with a message pointing to `age-keygen -pq`. `-pq` is the
  documented default.
- An identity file must be a regular file, not a symlink, owned by the
  effective uid, with no group or other permission bits (the `ssh` rule);
  it is opened with the same no-follow walk as secret files
  (`internal/safepath`). Errors name the path and the class, never key
  material.
- A sealed write with zero recipients is refused, never degraded to
  plaintext.
- The operator's own recipient is always included when the operator
  recipients file exists, so an operator can always open what they sealed.

**Rotation and revocation.** Plans are cheap to regenerate from the recipe
and the provider, so there is no re-wrap tool: rotate by generating a new
key pair, replacing the recipient line (inventory or recipients file) and
re-recording. Keep the old identity only as long as artifacts sealed to it
must still be applied, then delete it. Removing a recipient revokes nothing
already written: delete or re-seal those artifacts. The same applies to a
leaked destination identity: the retention rule ("delete once applied")
bounds the damage.

### Operator UX

```text
# seal for the operator (controller-local later apply, CI artifact kept for review)
gonf plan -o out -seal frontends_nsd
gonf apply -identity ~/.config/gonf/identity out/plan.age

# seal also for the destinations of a cluster (phase 2) and ship it by hand
gonf plan -o out -seal -for frontends frontends_nsd
scp out/plan.age rex@blowfish:
ssh rex@blowfish doas gonf apply -identity /etc/gonf/identity plan.age

# emergency path without gonf's decryption (any age identity, plugins included)
age -d -i key.txt out/plan.age | gonf apply -
```

| Command | Behaviour |
|---------|-----------|
| `gonf plan -o dir -seal [-recipient r]… [-for host\|cluster\|fleet]` | records into memory, encodes the push frame, seals to the union of `-recipient`, the operator recipients file and (phase 2) the inventory recipients of `-for`; writes `dir/plan.age`; prints `wrote dir/plan.age (N ops, M recipients)`. Warns when `dir` still holds a `plan.jsonl` or `blobs/` from an earlier plaintext run (never deletes it: the operator's file). `-seal` with `-stdout` or `-redacted` is refused (usage error, exit 2). |
| `gonf plan -o dir` (no `-seal`) | unchanged (062 behaviour, plaintext + warning). The warning gains a hint `use -seal`. Whether `-seal` becomes the default for sensitive plans when a recipients file exists is a separate, user-approved decision (phase 3). |
| `gonf apply [-identity f]… file` | sniffs the first line: `age-encryption.org/v1` means sealed, anything else is the existing JSONL path. A sealed file is decrypted and decoded like stdin apply (`plan.DecodePush` into `plan.NewApplyRunDir`), then applied; the run dir is wiped as for push. |
| `gonf apply [-identity f] -` | the same sniff on stdin (a sealed stream, a GONF-PUSH/1 frame, or bare JSONL). |
| Elevated chunks of a sealed apply | the unprivileged parent decrypts; the sudo/doas child keeps receiving a plaintext chunk in the private run dir (`defaultElevatedApply`), so root needs no identity. `sudo gonf apply` with root's own identity works the same with `-identity`. |
| `push`, `cluster`, `fleet`, `-preview` | unchanged: plans stay in memory and on SSH. |

Failure handling: the decrypted stream is read **to EOF before any op is
applied** (age authenticates the final segment only at EOF; `DecodePush`
already reads the whole plan section, which the implementation must keep
and test with a truncated and a bit-flipped file). No matching identity,
a wrong recipient type, a truncated or modified file: error, exit 1,
nothing applied, run dir removed. Messages name the file and the number of
recipients, never key material or plaintext.

### Schema, versioning and remote skew

- Plan schema: unchanged; no bump. The inner plan keeps v21/v22 and the
  existing header gate.
- Container version: the age header (`age-encryption.org/v1`) plus the
  inner `GONF-PUSH/1` magic. A future change to what is sealed changes the
  inner magic, not age.
- Capability probe: `gonf -sealed-version` prints `1` (like
  `-strict-preview-version`), so scripts and a later remote helper can check
  a destination before shipping a sealed file.
- Older gonf given a sealed file or stream: `gonf apply plan.age` fails at
  JSON decoding of the first line, `gonf apply -` falls back to bare JSONL
  and fails the same way. Both refuse before any op; the message is poor
  ("invalid character") but safe. There is no degrade path: gonf never
  falls back to writing or applying plaintext when sealing or opening fails.
- Push/preview skew: none in phases 1-3 (no wire change). Phase 4 changes the
  chunk stream and is covered by the existing push rule (a remote with an
  older release is upgraded by `EnsureRemoteGonf`; strict preview never
  sees blobs, so it is unaffected).

### Performance

Sealing adds, per recipient, one KEM encapsulation (microseconds for X25519,
tens of microseconds for ML-KEM-768) and about 1.2 KiB of header for a
hybrid recipient; the payload costs one ChaCha20-Poly1305 pass (well above
1 GB/s on current amd64) plus 16 bytes per 64 KiB. gzip, which the push
frame already runs, dominates by an order of magnitude. Memory matches push:
the frame is built from a `MemoryStore`, so a plan with large synced trees
is held in memory on both sides, as a push already is. The implementation
task adds a benchmark over a representative conf plan
(`frontends`/`garage_config`) rather than relying on these estimates.

## Out of scope

- **Encrypting push, cluster or fleet transport.** SSH already provides
  confidentiality in transit; the destination needs plaintext to apply, so a
  second layer would put the key next to the data.
- **Authenticity of a sealed plan.** age has no sender authentication and
  recipient public keys are public, so a sealed plan proves nothing about
  who made it. As today, trust in an artifact comes from where it is stored
  and who can write there. Signing (e.g. a stdlib `crypto/ed25519` signer key
  pinned per destination) needs its own design if unattended apply of
  artifacts from shared storage is ever wanted.
- Secret material written by ops on the destination (files, crontab lines),
  argv in the destination's process list, package-manager output, memory
  zeroization (Go gives no guarantee; gonf does not claim it). All
  unchanged from secrets.md.
- Automatic key distribution: push does not generate, fetch or install
  identities. Creating a destination key is an explicit operator action
  with its own authority (F10: no secret credentials placed on hosts
  implicitly).
- Re-wrapping existing artifacts, ASCII armor, age plugins inside gonf,
  passphrase (scrypt) recipients: YAGNI; the age CLI covers the rare case.
- Sidecar or per-op encryption (option E) and schema changes for it.

## Phased implementation (follow-up tasks)

Every task depends on this design being accepted (w82) and on 062 being
merged; none may start without the user's explicit approval. Each keeps the
062 client gate: existing gonf tests, and the conf and dotfiles clients
building, listing and recording plans byte-identically unless `-seal` is
passed.

| Phase | Task | Content |
|-------|------|---------|
| 0 | `0b2` | Warn when `gonf plan -o dir` writes a plaintext secret-bearing plan into a git worktree where `plan.jsonl` is not ignored. No crypto. |
| 1 | `1b2` | `plan/seal` package over `filippo.io/age`: recipient parsing and type policy, identity loading with owner/mode/no-follow checks, `Seal`/`Open`, tests (round trip, multi-recipient, wrong key, truncation, bit flip, refused ssh recipients). Adds the dependency; no CLI. |
| 1 | `2b2` | `gonf plan -o dir -seal [-recipient]…` with the operator recipients file; writes only `plan.age` from a `MemoryStore` push frame. Docs: secrets.md, plan.md. |
| 1 | `3b2` | `gonf apply [-identity]… <plan.age\|->` with magic sniffing, read-to-EOF before apply, run-dir staging reuse, elevated chunks unchanged, `gonf -sealed-version`. Docs. |
| 2 | `4b2` | Destination recipients: `WithPlanRecipient` on `Host`, `-for host\|cluster\|fleet` on `gonf plan -seal`, operator runbook for generating a key on a host and applying a shipped `plan.age`. |
| 3 | `5b2` | Optional, needs a user decision: make `-seal` the default for sensitive plans when an operator recipients file exists (`-plaintext` opt-out), and/or resolve the operator identity through the secret provider (`-identity-ref`). |
| 4 | `6b2` | Optional: seal a multi-chunk push's sticky-dir blobs to an ephemeral per-push X25519 key sent only on each chunk's stdin, which lifts 062's refusal of sensitive blobs in elevated chunks. |

Dependencies: `2b2`, `3b2` and `6b2` need `1b2`; `4b2` and `5b2` need `2b2` and `3b2`; `0b2` needs only 062.
