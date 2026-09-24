# Signed plan artifacts (design, task 7b2)

Status: **accepted (user approval 2026-09-24); phases signing-1 (task
`6g2`), signing-2 (task `7g2`) and signing-3 (task `8g2`) implemented,
later phases open.**
`plan/seal` has `Sign`/`SignAt`, `Verify`, `LoadSigner`,
`LoadTrustedSigners`, `GenerateSigner`/`WriteSignerFile` and the
`GONF-SIGNED-PLAN/1` envelope (with its signed `signed-at` time), and the
CLI can make a signer key (`gonf plan-signer-keygen`) and sign what it
seals (`gonf plan -seal -sign`), and `gonf apply` verifies a signed plan
before decrypting it (`-trusted-signers`, `-require-signed`,
`-max-signed-age`), with `gonf plan-verify` for the emergency path. The
exact choices the implementation made for the points this design left
open are in the "As landed" sections for `6g2`, `7g2` and `8g2` at the end
of "Recommended design". Nothing here lifts plan-encryption.md's gate: no
unattended entry point exists yet (see "The unblocking condition"). The
follow-up tasks are listed in the last section.

**Relationship to plan-encryption.md.** That design (task `w82`, phases
`0b2`-`3b2` implemented and merged) gives sealed plans (`plan.age`)
*confidentiality*: only a holder of a recipient's private key can read one.
Its own "Threat model" table names the gap this document closes:

> T10 — **Forgery / substitution** of a plan a later `gonf apply` trusts.
> Recipient public keys are public, and age has no sender authentication, so
> anyone can produce a `plan.age` that decrypts. Trust still comes only from
> where the file is stored and who can write there.

And its "Provenance" section states the resulting rule, which this document
exists to eventually satisfy:

> Gate: no unattended sealed apply. Nothing in gonf may apply a sealed plan
> without an operator choosing the file... That stays blocked until a
> signing design exists and is implemented (task `7b2`).

**Summary.** Add a thin, optional signing layer *around* the existing
`plan.age` artifact, in the same package that already owns the format
(`plan/seal`, task `1b2`): `Sign` wraps a sealed artifact's bytes in a small
envelope carrying an Ed25519 signature over those exact bytes; `Verify`
checks that signature against a destination's own locally-controlled set of
trusted signer public keys and, only on success, hands the original sealed
bytes back unchanged for the existing `Open`/decrypt path. Encryption and
signing stay independent, composable operations on the same underlying
`plan.age` bytes — signing never touches what is inside the ciphertext, and
an operator who only wants confidentiality (interactive apply, a human
chooses the file) keeps using plain `-seal` with no signature at all. A
signature does not add authorization by itself, either: it proves *which*
pinned key produced the bytes, and a destination decides in advance, by what
it puts in its own trusted-signers file, which keys that destination will
ever act on unattended. This is the same "no online service, no shared
secret, local files an operator explicitly places" philosophy age's own
identity and recipients files already use (plan-encryption.md "Keys"), kept
consistent rather than inventing a second trust model.

## Threat model

**What signing protects against.** Not a new adversary class from scratch —
the same T2 environment (`plan-encryption.md`'s "Threat model": copies of an
artifact outside gonf's control — a backup, a shared directory, a CI
artifact store, a compromised or merely careless third party with write
access to wherever a `plan.age` is dropped for pickup) but with a stronger
consequence once *anything* picks the file up and applies it without a human
choosing it each time: today, per T10, **anyone** who knows a destination's
public recipient key (which is meant to be shared — that is the whole point
of a public key) can produce a `plan.age` that decrypts cleanly on that
destination. A human operator running `gonf apply` interactively is the
control that currently stands between "a file that decrypts" and "a file
that runs": they chose to fetch and run this specific file. Remove that
human — a timer, a cron job, a pull agent polling a shared directory, a CI
step — and "decrypts cleanly" is the *entire* trust check, which T10 already
says is no check at all.

Concretely, signing must close the gap where any of these could otherwise
feed an unattended `gonf apply` a `plan.age` that decrypts under a real
recipient but was never produced by, or authorized by, the real operator:

- A compromised or misconfigured CI job (not the one meant to build plans)
  that has write access to the drop location.
- A backup or artifact-store restore that resurrects an *old*, superseded
  `plan.age` (see "Replay and rollback" below — signing alone, without a
  freshness check, does not close this one).
- Any other local or network actor who can write to, or race a write into,
  the location an unattended apply path reads from (T2's "copies of the
  artifact outside gonf's control" territory, now with a write path an
  automated consumer trusts by default).

| # | Adversary / scenario | Without signing | With signing (this design) |
|---|----------------------|------------------|------------------------------|
| S1 | Third party plants a fresh, validly-encrypted `plan.age` at the pickup location, forged from nothing but a public recipient key (T10) | Applies unattended with no check at all | Refused: no signature the destination's trusted-signers file recognizes |
| S2 | A signer's own controller or CI credentials/workstation is compromised, and the attacker signs a malicious plan with the real key | n/a (no signing exists yet) | **Not solved.** A signature only proves "produced by a holder of this private key," exactly like an SSH-signed commit or a code-signing cert; see "Out of scope / residual risks" |
| S3 | An old, previously-legitimate, still-validly-signed `plan.age` is replayed (restored from a backup, or an attacker who captured a past artifact resubmits it) after the recipe it encodes is known-bad or superseded | n/a | Bounded, not eliminated, by a signed freshness timestamp (baseline) or a monotonic counter (optional, stronger); see "Replay and rollback" |
| S4 | The destination's own trusted-signers file is itself writable by an untrusted account, or reachable through a symlink | n/a | **Not solved by signing itself** — this file needs exactly the same hardening `ce2` gave the recipients file, or S1 reopens through it; see "Keys" |
| S5 | A signed artifact for destination A's recipient set is stripped of its ciphertext and re-wrapped with a different, attacker-chosen ciphertext, keeping A's old signature | n/a | Rejected: the signature covers the *exact* ciphertext bytes (see "What is signed"), so any change to them, including a full replacement, invalidates it |

## Options compared

| Option | Verifiable before decrypting? | New dependency | Needs an online service | Verdict |
|--------|-------------------------------|-----------------|--------------------------|---------|
| A. Status quo (no signing) | n/a | none | no | already exists; does not close T10 for unattended use — why this document exists |
| B. Shared symmetric MAC (e.g. HMAC-SHA256 with one key every trusting destination also holds) | yes | none | no | rejected: the verification key *is* the signing key, so any destination trusted to verify could also forge a plan another destination would accept — the opposite of what a "third party compromised destination" threat needs; also reintroduces exactly the shared-secret-on-every-host shape F10 and plan-encryption.md option B already rejected for encryption |
| C. Ed25519 (`crypto/ed25519`, Go stdlib) | yes | **none** — already in the standard library | no | **recommended** |
| D. minisign/signify-compatible wire format | yes | none (still Ed25519 underneath) | no | inspiration, not a compatibility target: their format wraps a bare file, not an artifact that is already an age container with its own header; matching their exact bytes buys interop with the external `minisign`/`signify` tools but adds real format-fitting work for a benefit this design does not need (there is no equivalent to age's `age -d` emergency path for a proprietary GONF-PUSH/1 frame regardless) |
| E. X.509 / a real CA and certificate chain | yes (after chain validation) | a full PKI: CA, issuance, revocation lists or OCSP | usually yes (CRL/OCSP fetch) | rejected: enormous complexity and operational infrastructure disproportionate to gonf's local-file, single- or few-operator model; mirrors why plan-encryption.md rejected SSH host keys (option D there) for complexity and rotation fragility, several times over |
| F. Sigstore/cosign-style keyless signing (OIDC identity + transparency log) | requires network access to a transparency log at verify time | a real dependency tree | **yes, unconditionally** | rejected: every other piece of this feature area (identity files, recipients files, and now signer files) is a local file an operator places by hand, with no online service anywhere; keyless signing is the opposite of that, and gonf has no existing OIDC/service integration to build on |

**C (Ed25519) is recommended** for the same reasons `age` itself was chosen
in plan-encryption.md: a small, specified, widely-audited primitive, and
here even cheaper — it is already in Go's standard library
(`crypto/ed25519`, no `go.mod` change at all), produces small artifacts (32
byte public key, 64 byte signature), verifies in microseconds, and is the
same primitive SSH, git commit signing, WireGuard-adjacent tooling and
`minisign`/`signify` already use for exactly this purpose (proving "signed
by a holder of this private key," nothing more).

## Recommended design

### What is signed: encrypt-then-sign, over the exact `plan.age` bytes

The signature is computed over the **entire sealed artifact's bytes as
`plan/seal.Seal` (or its `-for` per-host variant) already produces them** —
the age header, every recipient stanza, and the full ciphertext — not over
the plaintext `GONF-PUSH/1` frame before sealing (sign-then-encrypt). This
is a real design choice with real trade-offs, not the only reasonable one:

| | Encrypt-then-sign (recommended) | Sign-then-encrypt |
|---|---|---|
| Verifiable before spending any decrypt effort | yes — Ed25519 verify is microseconds, and a forged or unsigned file is rejected before `age.Decrypt` ever runs | no — the destination must decrypt first (bounded by `be2`'s caps, but still real CPU/memory work spent on a file that may turn out to be unsigned or forged) |
| Binds the signature to a specific recipient set | yes — resealing identical plan content to a different recipient (or a future `-for` per-host artifact, task `4b2`) produces different ciphertext bytes, so an old signature can never be silently carried over; re-signing is required whenever re-sealing is | not automatically — a plaintext signature could, in principle, be attached to any re-encryption of the same content, which is *not* wanted here: each destination's artifact should need its own deliberate signing step |
| Touches `plan/seal.Seal`/`Open` or `plan.EncodePush`/`DecodePush` | no — signing wraps the *output* of the existing, already-reviewed `Seal`, so those functions and their tests are untouched | yes — the frame itself would need a new field, which is exactly the "inventing a parallel mechanism" the task brief warns against |
| Preserves the existing `age -d -i key plan.age \| gonf apply -` emergency path (plan-encryption.md "Keys") verbatim | **no** — a signed artifact is not a bare age stream any more; seeing this coming, the implementation phase must add a small, explicit unwrap step (`gonf plan -verify-only -unsign`, sketched below) so the emergency path still exists, just with one extra command first | yes, unchanged |

Encrypt-then-sign wins on balance: the ability to reject a forged artifact
*before* decrypting is the single most valuable property for the exact
scenario this design exists for (an unattended process pulling a file from
somewhere semi-trusted), and it keeps the change fully additive to
`plan/seal` rather than reopening `Seal`/`Open`. The lost bare-`age -d`
path is a real, acknowledged cost, closed by adding an explicit unwrap
command rather than by weakening the property above.

### Artifact: an envelope around the existing `plan.age` bytes

```
GONF-SIGNED-PLAN/1\n
<signer public key, 32 bytes>\n
<signature, 64 bytes>\n
<the complete plan.age bytes, unchanged, to EOF>
```

(Exact line encoding — raw base64 standard alphabet, one key/signature per
line, matching how `age` itself writes its own header lines — is an
implementation detail the phase-1 task picks; what matters for this design
is the three logical fields and that everything after them is the
untouched, existing sealed artifact. As landed, a fourth, signed field
follows the signature: the `signed-at` time "Replay and rollback" needs;
see "As landed".)

- The magic `GONF-SIGNED-PLAN/1` is a new, distinct first line, chosen so it
  is neither `age-encryption.org/v1` (a bare sealed plan) nor the first
  byte of a plaintext `plan.jsonl` line (`{`), exactly the disambiguation
  `isSealedPlanBytes` already relies on for the existing sniff
  (`internal/cli/cli.go`). `gonf apply`'s sniff extends to check for this
  magic *first*, and only falls through to the existing
  `age-encryption.org/v1` check on the bytes that follow once a signature
  has verified (see "Verification order" below); an input starting with
  neither magic is the existing plaintext-JSONL path, unchanged.
- **An unsigned `plan.age` stays fully valid input, unchanged, for
  interactive apply.** Signing is additive and optional: `gonf apply
  -identity key plan.age` on a bare, unsigned sealed artifact behaves
  exactly as it does today (`3b2`). Only a *separate*, explicitly-opted-in
  unattended entry point refuses an unsigned or unverifiable input — see
  "The unblocking condition."
- Old gonf given a signed envelope refuses it before any op, the same way
  it already refuses a sealed or plaintext skew today
  (plan-encryption.md "Schema, versioning and remote skew"): the first line
  is not valid JSON and is not `age-encryption.org/v1`, so
  `plan.DecodePlanBytes`/`DecodePush` fail exactly as they do for any other
  unrecognized first line, with no new code needed for that half of
  compatibility.
- One package still owns the format: `plan/seal` gains `Sign`/`Verify`
  alongside its existing `Seal`/`Open`, so CLI and `api` code still never
  import cryptographic primitives directly, and the envelope format is one
  reviewed place, matching the package's existing doc comment ("CLI and api
  code depend only on this package's own Recipient and Identity types").

### API sketch (illustration only — not implemented by this task)

```go
// plan/seal/sign.go (new file, mirrors seal.go's shape)

// Signer is a validated Ed25519 signing identity, as returned by
// LoadSigner and accepted by Sign. Loaded with the same internal/safepath
// no-follow walk plus owner/mode(0o077) check LoadIdentities already uses
// (private key material gets the strict rule).
type Signer struct{ inner ed25519.PrivateKey }

// TrustedSigner is one entry of a destination's trusted-signers file: a
// public key plus the human label its line carried (for logging only,
// never trusted for identity — comparison is by key bytes). The file's own
// on-disk line format (key encoding, an optional trailing label, "#"
// comments) is an implementation-phase choice, same as the envelope's line
// encoding above — the design-relevant fact is only that Key/Label exist
// and that Label is documentary, not authoritative.
type TrustedSigner struct {
	Key   ed25519.PublicKey
	Label string
}

func LoadSigner(path string) (Signer, error)
func LoadTrustedSigners(path string) ([]TrustedSigner, error)

// Sign wraps sealed (the complete output of Seal, or a per-host Seal from
// task 4b2) in a GONF-SIGNED-PLAN/1 envelope, signed by signer. It performs
// no encryption of its own and does not require identities to open
// anything: it operates purely on bytes Seal already produced.
func Sign(sealed []byte, signer Signer) ([]byte, error)

// Verify checks env against trusted (tried in order, the same way Open
// tries every identity until one matches), and on success returns the
// original sealed bytes unchanged — ready to pass to Open exactly as an
// unsigned plan.age would be. ErrSignatureUnrecognizedSigner names no key
// material, matching the
// package's existing "never echo key material" rule; the caller decides
// whether an unsigned input (no GONF-SIGNED-PLAN/1 magic at all) is
// acceptable — Verify itself is only ever called once that magic is
// already recognized.
func Verify(env []byte, trusted []TrustedSigner) (sealed []byte, signer TrustedSigner, err error)
```

`Sign`/`Verify` take and return whole `[]byte` values, not `io.Writer`/
`io.Reader` streams, deliberately matching `internal/cli/plan_seal.go`'s
existing `sealPushFrame` (already `([]byte, error)`, buffering the whole
sealed frame before writing it anywhere): Ed25519 signs an
already-in-memory message directly (it hashes internally; no manual
pre-hash step is needed), and every existing size bound in this feature
area (`be2`'s `maxSealedFrameBytes`, `plan.MaxDecompressedPushPlan`) already
assumes a plan is small enough to hold in memory once decrypted, so holding
the still-encrypted, smaller ciphertext in memory to sign or verify it adds
no new class of cost.

### Keys

| Key | Where | Made with |
|-----|-------|-----------|
| Signer identity (private) | wherever plans are produced — an operator's controller, or a CI system's own private, access-controlled storage | a new Ed25519 keypair, generated once (implementation phase adds a small `gonf plan-signer-keygen` convenience, since unlike age there is no existing external `age-keygen`-equivalent to shell out to) |
| Trusted signers (public) | **on each destination that will ever apply a plan unattended**, e.g. `/etc/gonf/signers` for a root-run unattended apply, analogous to the identity file's own default-path convention | copied, by the operator, from a signer identity's printed public key — the same explicit, by-hand distribution model plan-encryption.md already uses for recipients and identities ("Automatic key distribution" is out of scope there too) |

**Whose key signs: one flat trust set per destination in the first
implementation, not a
tiered scheme.** The task brief asks explicitly whether the human operator,
a CI/build system, or both should sign, with different trust levels. This
design's answer for the first implementation phase is: **a destination
trusts a *set* of signer public keys equally** — exactly how a destination
already trusts a *set* of recipient public keys equally (no recipient is
more "official" than another in the existing design). Whether a given key
belongs to a human operator's laptop or a CI runner is a fact about how
that private key is operated and stored, not something the protocol needs
to encode or weight: an operator who wants "CI can build but only a human
may authorize a privileged rollout" implements it by which keys go in which
destination's trusted-signers file, and by which pipeline holds which
private key — the same way file permissions, not the age format, already
decide who can *read* an identity file. A genuine multi-signer *threshold*
scheme (require signatures from N of M pinned keys before an unattended
apply proceeds) is a real, useful extension for a higher-assurance fleet,
but it is a meaningfully larger design (quorum bookkeeping, what happens on
partial signature sets, how threshold interacts with per-host `-for`
artifacts) and is left as an explicit, separate, later-phase task rather
than folded into this one — see "Phased implementation," phase signing-5.

**No default trusted-signers path assumed for root**, mirroring
plan-encryption.md's identity rule and for the identical reason: under
`sudo`/`doas`, which `HOME`/`XDG_CONFIG_HOME` apply is ambiguous, so an
unattended, typically-root apply path must be given
`-trusted-signers <path>` explicitly rather than defaulting to a guess.

**The trusted-signers file needs exactly `ce2`'s hardening, from day one,
not as a follow-up fix.** `ce2` found and closed a real vulnerability
where the *recipients* file (also public-key content) was read with a
plain `os.ReadFile`, letting a symlink or a world-writable file silently
add an attacker-controlled recipient. A trusted-signers file has the
identical shape and the identical consequence if it is not hardened the
same way: whoever can write it, or plant a symlink at its path, chooses
which signatures an unattended apply will ever accept. `LoadTrustedSigners`
must reuse `plan/seal`'s existing `internal/safepath` no-follow walk plus
an owner check and a refuse-if-group-or-other-writable rule — the same
policy `LoadRecipientsFile` already implements (`ErrRecipientsFileWritable`,
a narrower 0o022 group-or-other-*write* check, not the 0o077 check an
identity file needs — no group or other permission bit at all, read
included — since a signer's *public* key is not secret to read, only to
tamper with). This is not a new policy to invent; it is applying the one
`ce2` already wrote to a second file with the same trust shape.

**Rotation and revocation are file edits, not protocol features**, exactly
like an age recipient: to revoke a signer, remove its public key line from
every destination's trusted-signers file it appears in. A removed key's
past signatures remain mathematically valid forever (Ed25519 has no
built-in expiry), but any destination that has updated its own
trusted-signers file simply no longer recognizes them — the same "no CRL,
no online revocation service, an operator edits a local file" model as
recipient rotation, and for the same underlying reason: there is no
existing service in this project's design for anything stronger, and
adding one would be exactly the kind of infrastructure option E/F above
were rejected for. **Recipient rotation and signer rotation are
independent of each other** but interact once an artifact is re-sealed: a
freshly re-sealed `plan.age` (new recipient, or the same content re-sealed
for any reason) produces different ciphertext bytes, so its old signature
is no longer valid over the new bytes (see "What is signed") and it must be
signed again — there is no way to carry a signature forward across a
reseal, by design.

### Replay and rollback

A signature alone answers "did a trusted key produce these bytes," not
"is this still the plan I want applied right now." A validly-signed
`plan.age` from months ago — superseded, reverted, or describing a
configuration since found to be wrong — remains exactly as validly signed
today as the day it was made, so a naive unattended-apply path is exposed
to a classic **replay/rollback** attack (or, just as realistically, an
honest mistake: a backup restore reintroducing a stale artifact) unless
something bounds freshness. This is the same category of problem package
mirrors and update systems solve with a "timestamp"/"snapshot" role (e.g.
TUF, or `apt`'s `Release` file `Date`/`Valid-Until`), scaled down to what a
single small tool needs:

| Approach | State needed on the destination | Prevents replay within the window | Prevents replay after expiry |
|---|---|---|---|
| None (signature only) | none | no | no |
| **Signed freshness window (recommended baseline, task signing-3)** | none — just the destination's own clock | no (a captured, still-fresh artifact can be replayed until it expires) | yes |
| Monotonic sequence counter (optional, stronger, task signing-4) | a small local ledger (last-applied sequence number per signer, or per plan id) | **yes** | yes |

**Baseline (task signing-3): a signed freshness timestamp.** The envelope (or,
equivalently, a field inside it covered by the signature) carries a
`signed-at` timestamp; verification refuses an envelope older than a
configured tolerance (a reasonable default: on the order of the shortest
sane unattended-apply interval, e.g. an hour to a day, operator-tunable).
This needs no persistent state on the destination — only a synced clock,
which every host already needs for TLS/certificate validation and for
`plan-encryption.md`'s own T4 discussion of long-lived artifacts, so it
adds no new operational requirement. It bounds the *damage* of a captured
artifact (it stops working after the window) without fully closing replay
*within* that window.

**Optional, stronger (task signing-4): a monotonic counter.** For a destination
that wants a hard anti-replay guarantee, not just a bounded window, the
signer embeds a strictly increasing sequence number (or the operator's own
plan-recording timestamp, if guaranteed monotonic per signer) and the
destination persists the highest sequence number it has ever accepted from
each trusted signer (e.g. one small file per signer under
`/var/lib/gonf/`), refusing anything not strictly greater. This is a real
extra piece of destination-side state — the first persistent,
apply-affecting state this whole feature area would introduce — and is
therefore left for a dedicated, separately-approved follow-up rather than
folded into the first signing task; see "Phased implementation," phase
signing-4.

### Verification order and interaction with existing decryption

```
gonf apply -identity ... [-trusted-signers path] [-require-signed] <plan.age|->
```

1. Sniff the first line. `GONF-SIGNED-PLAN/1` → signed path (below);
   `age-encryption.org/v1` → today's unsigned sealed path (`3b2`),
   refused outright if `-require-signed` was given; anything else →
   today's plaintext path, likewise refused under `-require-signed`.
2. **Signed path:** parse the two header lines (public key, signature).
   Load trusted signers (`-trusted-signers`, or the same
   root-must-be-explicit rule plan-encryption.md's identity flag already
   has). `Verify` the signature over the remaining bytes — **before any
   `age.Decrypt` call** — against the trusted set; the embedded public key
   is compared by exact bytes to each entry, never trusted on its own
   authority (a plan's own claim of who signed it means nothing until it
   matches something the destination already had). On success, and only
   then, check the freshness window (or the phase-2 counter). Any failure
   here — unrecognized magic under `-require-signed`, no matching trusted
   key, a bad signature, a stale timestamp — refuses immediately, exit 1,
   nothing decrypted, nothing applied, wording that never says "verified"
   about anything downstream it did not itself check (matching
   plan-encryption.md's "Provenance" wording discipline).
3. Once verified, the unwrapped bytes are the **exact same bytes**
   `-seal`'s output always was. Everything from here is unchanged, existing
   `3b2`/`be2` code: sniff `age-encryption.org/v1`, decrypt with
   `-identity`, read to EOF under `maxSealedFrameBytes` before applying
   anything, decode with the existing size-capped `plan.DecodePush`. Signing
   adds a check *in front of* decryption; it does not change decryption.

**Emergency path.** Since a signed artifact is no longer a bare age stream,
the existing `age -d -i key plan.age | gonf apply -` path (plan-encryption.md
"Keys") needs one new, explicit step for a signed file: a small
`gonf plan -verify-only -trusted-signers path plan.age` (or equivalent)
that checks the signature and writes the unwrapped `plan.age` bytes back
out, after which the original emergency path works unchanged. This is new
surface the implementation phase must add; it is called out here so
accepting this design means accepting that small addition too, not
discovering it midway through `3b2`'s signing successor. As landed (task
`8g2`) it is its own subcommand, `gonf plan-verify`, rather than a `gonf
plan` flag, because `gonf plan`'s positional arguments are task names.

### The unblocking condition

This is the concrete answer to plan-encryption.md's own gate: **nothing
lifts "no unattended sealed apply" merely because a signing library
exists.** A specific future task that wires an unattended entry point
(a timer unit, a documented cron/pull-agent recipe, a CI step) may only do
so once **every** one of these is true, and should say so explicitly in its
own gate/test coverage:

1. The input is refused outright unless it sniffs as the
   `GONF-SIGNED-PLAN/1` envelope — a bare, unsigned `age-encryption.org/v1`
   stream is never accepted on this path, no matter how it decrypts
   (`-require-signed`, unconditionally set by the entry point itself, never
   left to an operator default — the same discipline `3b2` already applies
   to `-identity` for root).
2. The embedded signer key is checked against a trusted-signers file that
   is **local to, and controlled by, the destination itself** — never a
   key or a trust decision the incoming plan carries or implies about
   itself.
3. `Verify` succeeds over the artifact's exact bytes, checked **before**
   any `age.Decrypt` call is made.
4. A freshness check (the phase-1 timestamp window, or a stronger phase-2
   counter if that destination opts into it) passes — an old, replayed,
   otherwise-validly-signed artifact is still refused.
5. The trusted-signers file itself has passed the same
   symlink/owner/writability hardening `ce2` already gave the recipients
   file (see "Keys") — verified by that file's own loader, not assumed.
6. All of the above is exercised by tests the same way `1b2`/`2b2`/`3b2`/
   `ce2`/`be2` exercised the encryption half: round trip, wrong signer,
   tampered ciphertext after signing, expired timestamp, missing/writable
   trusted-signers file, and — mirroring `ce2`'s adversarial-probe
   self-review — an actual reproduction of each refusal against a
   pre-fix build before trusting that the fix closes it.

Only once a specific task delivers all six, for the specific unattended
entry point it adds, does that path's use of `-require-signed` amount to
the operator-approved unblocking plan-encryption.md's "Provenance" section
asked for. This document does not itself unblock anything; it is the
prerequisite `1b2`-shaped library work that such a future task would build
on, the same relationship `w82`'s design had to `1b2`.

### As landed (task `6g2`, phase signing-1)

The library half, in `plan/seal` (`sign.go`, `signer.go`, and `keyfile.go`
for the shared file hardening), with the choices this design left open:

- **API**, as sketched, except that `Verify` returns a struct since task
  `7g2` added the signing time: `Sign(sealed []byte, signer Signer)
  ([]byte, error)` (the current time) and `SignAt(sealed, signer, at
  time.Time)` (an injected clock), `Verify(env []byte, trusted
  []TrustedSigner) (Verified, error)` with `Verified{Sealed, Signer,
  SignedAt}`, `LoadSigner(path) (Signer, error)`,
  `LoadTrustedSigners(path) ([]TrustedSigner, error)`, plus
  `Signer.Public()` (the trusted-signers entry for that key),
  `TrustedSigner.String()` (its exact file line) and the exported
  `SignedPlanMagic` for a caller's sniff. A `Signer` never prints its
  private key: printed directly it shows only its public key under every
  `fmt` verb, and the key is held only by a closure, so it cannot be
  reached when the `Signer` sits nested in an unexported struct field,
  where `fmt` cannot call its `Format` method.
- **Envelope lines:** the magic, then the 32-byte key and the 64-byte
  signature each as one line of unpadded standard base64 (43 and 86
  characters), decoded strictly and at their exact length, then the
  signed-at line (below), so an envelope has one accepted spelling:

  ```
  GONF-SIGNED-PLAN/1\n
  <43-character base64 public key>\n
  <86-character base64 signature>\n
  signed-at 2026-09-24T10:50:51Z\n
  <plan.age bytes, to EOF>
  ```

  The payload must start with
  `age-encryption.org/v1\n` (`Sign` refuses anything else, and `Verify`
  never returns anything else, so a signed plaintext plan or a nested
  envelope cannot come out as "sealed").
- **Signed-at (task `7g2`).** `signed-at <time>`, where the time is RFC
  3339 in one spelling only: UTC with a literal upper-case `Z`, second
  precision, no fraction, a four-digit year (`YYYY-MM-DDTHH:MM:SSZ`, 20
  characters). `SignAt` converts to UTC and truncates to the second, and
  refuses the zero time or a UTC year outside 0000-9999
  (`ErrSignedAtInvalid`). `Verify` accepts only a line that formats back to
  itself byte for byte, so a lower-case `z`, a numeric offset, a fraction,
  a missing field or an out-of-range one (month 13, second 60) is
  `ErrEnvelopeMalformed` even when correctly signed. `Verify` returns the
  time (`Verified.SignedAt`, UTC) but enforces no freshness window; that
  check, against the destination's clock, is task `8g2`. The field was
  added to `/1` itself, not as a `/2`, because no gonf binary had
  produced a `/1` envelope yet: `7g2` added it before the first producer
  (`gonf plan -seal -sign`) existed. The one exception is v0.17.0's
  `plan/seal` library, whose `Sign` (no CLI) still wrote the undated
  layout. Such an envelope fails closed here: its fourth line is the age
  header, not a `signed-at` line (`ErrEnvelopeMalformed`), and its
  signature covers a different message, so it can never verify.
- **Signed message:** the magic line, then every byte after the signature
  line: `"GONF-SIGNED-PLAN/1\n" || "signed-at <time>\n" || plan.age`, pure
  Ed25519 (RFC 8032). So the time is authenticated (a replayed envelope
  cannot be relabelled as fresher), and the magic prefix binds the envelope
  version, so a signature cannot be reused under another version or a
  format that signs the bare bytes; an Ed25519ctx/Ed25519ph signature of
  the same message, and one over the message without the signed-at line,
  are refused.
- **Weak keys refused.** Go's `ed25519.Verify` accepts a small-order public
  key, and for one of those anyone can forge a signature without a private
  key: R=identity, S=0 verifies every message for the identity point, and
  a random R=[S]B verifies for the all-zero key (a likely placeholder)
  about one try in four. So a trusted key must be canonical and must not
  have small order ([8]A is not the identity, as "Taming the many EdDSAs"
  recommends). The check uses `math/big` (`plan/seal/edpoint.go`) instead
  of adding `filippo.io/edwards25519` as a dependency. It runs when the
  file is loaded (`ErrTrustedSignerWeakKey`), on the envelope's own key
  (`ErrEnvelopeMalformed`) and on every trusted entry `Verify` matches, so
  a `TrustedSigner` a caller builds itself cannot get around it.
- **Key files:** `GONF-SIGNER-SECRET-ED25519 <base64 seed>` (exactly one per
  signer file) and `gonf-signer-ed25519 <base64 key> [label...]` (one per
  trusted signer; at least one, or `ErrNoTrustedSigners`). Blank lines and
  `#` comments are ignored, a file is read up to 64 KiB, and a key of any
  other type (an age key, an `ssh-ed25519` line, the other file's line) is
  refused by class. Fields are split on ASCII spaces and tabs only, and a
  CRLF line ending is accepted. Any other white space inside a line (a CR,
  as in a CR-only file, or a Unicode space) is refused, so two entries can
  never fold into one line. A trusted key may not repeat
  (`ErrTrustedSignerDuplicate`). A label must be printable and must not
  contain a type word or a key-shaped field. A leading UTF-8 byte-order
  mark gets its own error (`ErrKeyFileBOM`). Both files go through the identity/recipients files'
  no-follow walk and owner check; the signer file refuses any group/other
  bit (0o077), the trusted-signers file only group/other write (0o022).
  Every refusal names the path, line number and class, never content.

### As landed (task `7g2`, phase signing-2)

The signing half of the CLI (its verifying side is task `8g2`, below):

- **Keygen.** `gonf plan-signer-keygen <signer-file>` generates a key
  (`seal.GenerateSigner`, `crypto/rand`) and creates the file with
  `seal.WriteSignerFile`: `O_CREAT|O_EXCL|O_NOFOLLOW`, mode `0600`, in a
  parent reached by the same no-follow walk `LoadSigner` uses, so it never
  replaces an existing file (an older key may still be needed) and never
  writes through a symlink at the path or above it; a missing parent is
  not created. The file is two `#` comments (what it is, and its public
  line) plus the one secret line. Stdout gets only the
  `gonf-signer-ed25519 <key>` trusted-signers line, so it can be appended
  to a destination's file; the "wrote" report goes to stderr. The secret
  is never printed.
- **Signing.** `gonf plan -seal -sign <signer-file>` loads the signer with
  `LoadSigner` before anything is recorded and signs every sealed artifact
  right after sealing it, before it is staged or printed: plain `-seal`,
  `-seal -stdout`, and each host's own `plan-<host>.age` with `-for` (one
  signature per ciphertext, so no two hosts share one). Without `-sign`
  the output is byte-identical to before. `-sign` without `-seal`, or with
  an empty path, is a usage error (exit 2); a signer file `LoadSigner`
  refuses is exit 1, `plan: -sign refused: <path, line and class>; nothing
  written`, never its content. There is no default signer path.
- **Report.** The "wrote" line gains `, signed <signed-at, RFC 3339 UTC>`
  and a `  signer gonf-signer-ed25519 <key> sha256:<16 hex>` line under
  the recipients. The 43-character key is printed whole (short enough,
  unlike an `age1pq` recipient) and the fingerprint is the SHA-256 of that
  line, the same `sha256:` form the recipient lines use (task `4g2`),
  reproducible with `printf %s 'gonf-signer-ed25519 KEY' | sha256sum`. The
  wording is "signed", never "verified": nothing on the producing side
  checks anything.

### As landed (task `8g2`, phase signing-3)

The verifying half of the CLI (`internal/cli/apply_signed.go`,
`plan_verify.go`), in front of the unchanged `3b2`/`be2` sealed path:

```text
gonf apply [-identity f]... [-trusted-signers f]... [-require-signed] [-max-signed-age 24h] <plan.age|->
gonf plan-verify [-trusted-signers f]... [-max-signed-age 24h] <signed-plan|->   # bare plan.age to stdout
gonf -signed-version                                                             # prints 1
```

- **Sniff.** The first bytes decide the path, signed first: a
  `GONF-SIGNED-PLAN/` prefix of any version (`seal.LooksSigned`) is the
  signed path, so an envelope this binary cannot verify (another version,
  v0.17.0's undated `/1`) is refused as malformed instead of falling
  through to another decoder; then `age-encryption.org/v1` (unsigned
  sealed); anything else is plaintext. The stdin peek is 21 bytes, which
  covers both magics.
- **Signed path.** Load the trusted signers, `seal.Verify`, then the
  freshness window, all before `age.Decrypt` and before any run directory
  exists; the verified `plan.age` then goes through the existing sealed
  path byte for byte. A signed stdin stream is read to EOF first, capped
  at `maxSignedEnvelopeBytes` (the 512 MiB frame cap plus 1/1024 plus
  1 MiB, room for age's overhead), since the signature covers every byte.
  `-strict-preview` and `-apply-dir` are refused with a signed stream
  (exit 2), as with a sealed one.
- **A signed plan is always verified.** Without `-trusted-signers` the
  non-root default `${XDG_CONFIG_HOME:-$HOME/.config}/gonf/trusted-signers`
  is used, and root must pass `-trusted-signers` (the `-identity` rule).
  A signature is never stripped unchecked, with or without
  `-require-signed`: before this task a signed envelope was refused as
  undecodable, so verifying it is no new refusal for anyone.
- **`-require-signed`** refuses an unsigned `plan.age`, a `plan.jsonl`, a
  push frame and bare JSONL (exit 1, `apply: -require-signed: <src> is
  ...; nothing decrypted or applied`), from a stdin peek alone. Without
  it, unsigned input behaves exactly as before; with only
  `-trusted-signers`, an unsigned input is applied with a warning naming
  `-require-signed`, since the design keeps an unsigned `plan.age` valid
  for interactive apply.
- **Freshness window.** Checked only after the signature verified, against
  this host's clock: refused when `signed-at` is more than
  `-max-signed-age` (default 24h, the top of "an hour to a day"; must be
  positive) in the past, or more than 5 minutes (`signedClockSkew`, fixed,
  Kerberos' default tolerance) in the future. Exactly `-max-signed-age` old
  is accepted. A bad signature on a stale envelope is reported as a bad
  signature, never as stale.
- **Wording.** A refusal is `apply: <src>: signed plan refused: <reason>;
  nothing decrypted or applied`, exit 1; reasons name paths, line numbers,
  classes and the (non-secret) times, never key material or plaintext. Only
  after both checks pass does stderr say `signature verified: trusted
  signer gonf-signer-ed25519 <key> sha256:<16 hex> [(label)], signed <time>
  (within -max-signed-age <d>)`; the summary line after it is the
  unchanged `decrypted and applied ...`.
- **`gonf plan-verify`** runs the same checks and, only when they pass,
  writes the untouched `plan.age` to stdout (the report goes to stderr),
  so `gonf plan-verify -trusted-signers f signed.age | age -d -i key | gonf
  apply -` is the emergency path. It decrypts nothing and writes nothing
  for a refused input.
- **Not lifted.** `-require-signed` is only a flag an operator may pass;
  no timer, cron or pull agent sets it. plan-encryption.md's gate stays
  until signing phase 6 wires such an entry point (task `bg2`).

### Schema, versioning and remote skew

- No plan schema change: signing wraps `plan.age` bytes, which already
  wrap the unchanged `GONF-PUSH/1`/plan-JSONL frame. Nothing inside the
  ciphertext changes shape.
- New container magic: `GONF-SIGNED-PLAN/1`, alongside the existing
  `age-encryption.org/v1` (sealed) and bare-JSON (plaintext) first-line
  dispatch. A future change to the envelope's own fields would bump this
  magic, not age's.
- Capability probe: a `gonf -signed-version` printing `1`, matching the
  existing `-sealed-version`/`-strict-preview-version` pattern, so a
  runbook or unattended-apply wrapper can check a destination's gonf
  supports signed input before shipping one.
- Older gonf given a signed envelope refuses it before any op, with no new
  code required for that refusal (see "Artifact").

### Performance

Ed25519 signing and verification are each on the order of tens of
microseconds regardless of message size (the library hashes the message
internally; there is no separate large-input cost the way gzip or the AEAD
payload pass have) — negligible next to the sealing costs
plan-encryption.md's own "Performance" section already accounts for
(gzip, the per-recipient KEM encapsulation, the ChaCha20-Poly1305 pass).
The envelope adds a fixed, small overhead per artifact: a 32-byte public
key, a 64-byte signature, and a short timestamp field, on the order of 100
bytes total — irrelevant next to the ~1.46 KiB per-recipient age header
plan-encryption.md already measured.

## Out of scope / residual risks

Written with the same discipline plan-encryption.md used for its own "Out
of scope" section: naming what this design does **not** claim, not
implying it by omission.

- **Does not replace sealing.** Signing says nothing about who may *read*
  an artifact; that is still entirely `plan/seal`'s `Seal`/`Open` and its
  recipient policy. The two are independent and both required together for
  "only this destination can read it, and only from a key I trust."
- **Does not protect a signer's own compromised private key or
  workstation/CI credentials (S2).** A signature proves "produced by a
  holder of this private key," exactly like an SSH-signed commit or a code
  signing certificate — it is bounded by how well that key is guarded, not
  by anything this design adds. Rotation (see "Keys") is the only recourse
  after a suspected compromise, same as for an age identity.
- **Does not provide non-repudiation or revocation infrastructure.** There
  is no CRL, no OCSP, no transparency log (option F was rejected
  specifically to avoid needing one). Revocation is editing a local file on
  every destination that must stop trusting a key, and takes effect only
  where and when that edit is applied.
- **Does not fully close replay/rollback on its own** — only the baseline
  freshness window does, and only within its own tolerance; the stronger
  monotonic-counter option is a separate, later, explicitly-approved task
  (see "Replay and rollback").
- **Does not add any new authorization model on the destination.** Exactly
  as plan-encryption.md's "Operator UX" section states (its "Privilege:
  single process, as plain file apply" bullet) for sealed apply, a
  signed-and-verified plan is applied with the *invoking* process's
  privilege (`sudo`/`doas gonf apply ...`); signing decides whether the
  bytes are trusted to look at, not what they are allowed to do once
  applied.
- **Does not protect a trusted-signers file that is not itself hardened**
  (S4) — this design specifies that hardening as a required part of
  `LoadTrustedSigners`, not an optional extra, but the guarantee only holds
  once that loader is actually used everywhere a trusted-signers file is
  read.
- **Does not make the pickup location itself trustworthy for
  availability.** An adversary who can write to (or deny writes/reads at)
  an unattended path's drop location can still deny service — refuse to
  place a new artifact, or serve a stale/corrupted one — even once they can
  no longer make gonf *apply* something malicious. Signing is an
  integrity/authenticity control, not an availability one (matching how
  plan-encryption.md's own "be2" memory-bound fix is explicitly framed as
  availability protection only).
- **Does not itself decide the multi-signer/threshold question** (a
  destination requiring N of M signatures) — flagged as a real, useful,
  separately-sized extension in "Keys," left for a later task.

## Phased implementation (follow-up tasks)

Mirroring plan-encryption.md's own phase table. Every task below depends on
this design being accepted (`7b2`) and needs the user's explicit approval
before it may start, exactly as `0b2`-`6b2` required for `w82`. **None of
these may be started as part of this task**, and filing them here is task
creation only, not approval to begin.

| Phase | Content |
|-------|---------|
| signing-1 | `plan/seal` gains `Sign`/`Verify`, `LoadSigner`, `LoadTrustedSigners`, the `GONF-SIGNED-PLAN/1` envelope; tests for round trip, wrong signer, tampered ciphertext, unsigned input handling, and the same hardened-file probes `ce2` ran against the recipients file, run here against the trusted-signers file from day one. No CLI changes, mirroring `1b2`. |
| signing-2 | `gonf plan -seal -sign <signer-identity>` CLI wiring; a `gonf plan-signer-keygen` convenience command; docs. Mirrors `2b2`. **Done (task `7g2`)**, together with the envelope's `signed-at` field. |
| signing-3 | `gonf apply -trusted-signers ... [-require-signed]` CLI wiring, sniff-dispatch extension, the freshness-window check, `gonf -signed-version`; the `gonf plan -verify-only` unwrap helper for the emergency path. Mirrors `3b2`. This is the task the "unblocking condition" section's six checks land in, but landing it still does **not** by itself unblock any unattended path — no such path exists yet. **Done (task `8g2`)**; the helper landed as `gonf plan-verify`. |
| signing-4 (optional, needs a user decision) | Monotonic anti-replay counter (destination-side persistent state); this is the first piece of apply-affecting persistent state this feature area would add, so it is deliberately not bundled into signing-3. |
| signing-5 (optional, needs a user decision) | Multi-signer/threshold trust (require N of M pinned signers); per-destination signer pinning via inventory (`WithPlanSigner`, mirroring `4b2`'s `WithPlanRecipient`) once `4b2` itself lands. |
| signing-6 (optional, needs a user decision, and needs signing-3) | An actual unattended entry point (a documented timer unit / cron / pull-agent recipe) that sets `-require-signed` unconditionally and satisfies every item in "The unblocking condition" — the task that would finally lift plan-encryption.md's gate, for that one specific path, not as a side effect of any earlier phase. |

Dependencies: signing-2 and signing-3 need signing-1; signing-4 and
signing-5 need signing-3; signing-6 needs signing-3 (and signing-4/5 if the
destination in question opts into their stronger guarantees); signing-1
needs only this design (`7b2`) being accepted.
