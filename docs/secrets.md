# Secrets and secret providers

gonf resolves secrets on the **controller**, while a task body is recorded.
Recipes read them with `MustSecret` / `OptionalSecret` (strings, the
long-standing helpers) or `ResolveSecret` (bytes plus a typed error), and
`SecretFile` manages a file that is exactly one secret. All of them go
through one **secret provider**, configured once per process, and every
value they return makes the plan ops carrying it sensitive (see the last
section).

## Default: the file provider

Without configuration the provider is `secret.FileProvider{}`: it reads the
regular file `secrets/<path>` relative to the recipe's working directory —
the behaviour `MustSecret` / `OptionalSecret` have always had:

- bytes are returned exactly (leading/trailing whitespace, newlines, NULs);
- a leading `/` stays below `secrets/` (Rex compatibility):
  `MustSecret("/var/nsd/key")` reads `secrets/var/nsd/key`;
- a path that is empty or escapes `secrets/` is refused, and so is a symlink
  anywhere on the way (the directory `secrets/` included) and a final
  component that is not a regular file. The walk opens every component
  relative to its parent's descriptor with `O_NOFOLLOW`
  (`internal/safepath`), so a checked path cannot be raced into a symlink;
- errors name the path and the failure class, never the contents.

`secret.FileProvider{Dir: "vault"}` reads below another single directory
component of the working directory instead.

## Typed errors

A provider returns `*secret.Error`, whose `Kind` is one of:

| Kind | Meaning | File provider cases |
|------|---------|---------------------|
| `secret.ErrNotFound` | the store is usable but holds no such secret | the file, or a directory below `secrets/` on its way, is absent |
| `secret.ErrInvalid` | the reference or what it names is unacceptable | empty or escaping path; symlink; non-directory on the way; not a regular file; an empty value (refused by the api helpers) |
| `secret.ErrUnreadable` | the secret exists but cannot be read | permission denied; read I/O error |
| `secret.ErrUnavailable` | the store itself is unusable | the `secrets/` directory itself is missing or cannot be searched (permission denied); misconfigured `Dir` (more than one component, or `..`); for other providers: locked, unauthenticated, corrupt, crashed, timed out |

**Decide with `secret.IsNotFound(err)` only**, applied directly to the error
`ResolveSecret` / `secret.Resolve` returned. `IsNotFound` and `KindOf` read
the kind of a top-level `*secret.Error` and nothing else: a typed error
wrapped in another error has no kind for them, so neither a caller's own
`fmt.Errorf` wrapping nor a not-found buried in the cause of another failure
can make a broken store look like an absent secret. Do not use
`errors.Is(err, secret.ErrNotFound)` for that decision: it also matches
causes. The missing-`secrets/` error carries no `ENOENT` cause, so it does
not match `fs.ErrNotExist` either.

`secret.Resolve` wraps every provider call:

- it refuses to start on a done context and drops bytes (or a typed error) a
  provider returns once the caller's context is done; that yields an error
  wrapping `ctx.Err()` and no `*secret.Error`;
- it passes through only a top-level `*secret.Error` with a known kind whose
  `Ref` is the requested reference. Everything else becomes `ErrUnavailable`
  with the original error as cause: an unclassified error, a typed error
  wrapped by the provider, a typed error about another reference (e.g.
  `ErrNotFound` for the store's own unlock file), and a context error of
  the provider's own (its subprocess timeout) while the caller's context is
  still live. An adapter bug is never read as "not found".

**Optional means not-found only.** `OptionalSecret` returns `("", false)` for
`ErrNotFound` and nothing else; every other kind, an empty value and a
cancelled context fail plan recording exactly like `MustSecret`.

Behaviour correction (z52, from the 2026-09-21 bug sweep): a missing `secrets/` directory used to read as
"this secret is missing", so `OptionalSecret` silently dropped every
secret-backed fragment when gonf ran from the wrong working directory, and
`MustSecret` reported a misleading `secret "…" is missing`. It is now
`ErrUnavailable` (`secret "<path>": secrets directory "secrets" not found in
the working directory`) for both helpers. A missing file or subdirectory below
an existing `secrets/` is still "missing", with the same message as before.
All other messages are unchanged; a `secrets/` directory that cannot be
searched keeps its `open secret "<path>": permission denied` message but is
now `ErrUnavailable` (a store failure) rather than `ErrUnreadable`. Both
helpers failed on it before as well. Likewise a `secrets/` directory that is
removed, renamed or replaced while a lookup below it is running is
`ErrUnavailable` (`secret "<path>": secrets directory "secrets" was removed
or replaced during the lookup`), not "missing".

## Configuring another provider

```go
func main() {
    api.SetSecretProvider(secret.NewSnapshot(myProvider)) // once, first
    tasks.Register()
    os.Exit(cli.CLI())
}
```

- `SetSecretProvider` must be called once, at the composition root: calling
  it twice, with `nil`, from a task body, or after any secret was resolved
  fails fast (`logger.Fatal`), so one invocation never mixes secret sources.
- A provider implements `Resolve(ctx, secret.Ref) ([]byte, error)`, honours
  `ctx`, returns typed errors and never puts secret bytes into errors or logs.
  Keep adapters (e.g. an argv-invoked foostore) out of recipe and resource
  packages; `secret.ProviderFunc` adapts a plain function.
- `secret.NewSnapshot(p)` resolves each reference at most once and caches
  successes and not-found; installed with `SetSecretProvider` it lasts for the
  rest of the process, so every task, host and privilege chunk of one
  invocation sees the same value even if the store rotates meanwhile.
  Transient failures are retried. References are cached in canonical form
  (cleaned, without leading slashes or backslashes — `"/a/b"`, `"\a/b"` and
  `"a/b"` share one entry, as `FileProvider` and the pre-provider helpers
  read the same file for all three; a backslash elsewhere, as in `"a\b"`, is
  an ordinary name character), so a
  provider behind a Snapshot must treat those spellings alike; a cached
  not-found served for another spelling names that spelling in its `Ref` and
  message. Build it only with `NewSnapshot`: a zero `secret.Snapshot{}` is
  refused by `SetSecretProvider` like a nil provider. Each reference
  resolves independently: a slow one does not block others, and a caller
  waiting for someone else's resolution of the same reference stops when its
  own context is done. Every returned slice is a copy. The default file
  provider is deliberately not wrapped, to keep its read-on-every-call
  behaviour.
- `ResolveSecret` may be called from several goroutines; the provider
  configuration is locked. Providers themselves must then be safe for
  concurrent use (`Snapshot` and `FileProvider` are).
- `ResolveSecret(ctx, ref)` returns the error instead of stashing it and works
  outside recording too. `MustSecret` / `OptionalSecret` resolve with
  `context.Background()`: plan recording carries no context yet.

## What reaches the plan: secret-aware plans (task 062)

Resolving a secret records nothing by itself; only what a recipe places into
a resource reaches the plan, and there it stays **plaintext**: the destination
has to write it. Base64 (`content_b64`, member content) is an encoding, not
encryption, and nothing in gonf encrypts a plan. What gonf adds is
**sensitivity**: plan schema 22 marks every op that carries secret material
with `"sensitive": true`, and every output path treats such an op as secret.

### How an op becomes sensitive

Every value `ResolveSecret` returns — and so every `MustSecret`,
`OptionalSecret` and `SecretFile` value — is remembered for the rest of the
process (`secret.Values`). While a plan is recorded, **every string of every
op** is scanned for those values: resource ops as their drafts are packaged,
control ops (`when_begin` predicates and requirements) once the plan is
complete. The walk is by reflection over `plan.Op`, and each string field is
classified (`api/secret_fields.go`; a fitness test fails when a new field is
added unclassified):

| Class | Fields | On a match |
|-------|--------|------------|
| payload | decoded `content_b64` and member contents, the bytes of a packaged file source, template data strings (and their base64 decoding — how a `[]byte` is recorded and rendered), `template_param`, lines, argv, environment keys and values, cron command/schedule/environment lines, guard argv and expected output, validator argv, systemd calendar/boot delay/descriptions, `when` predicate values | the op is marked sensitive |
| identity | IDs, names, paths, symlink/hardlink targets, dependency and watch IDs, `After`/`Wants` units, binaries (`bin`, guard and validator binaries), working directory, `Creates`, home, source/staging/chroot directories, config set member keys and paths, `path_exists` predicates, requirement text | a strong secret (below) refuses the record; a weaker match marks the op sensitive |
| metadata | op kind, blob reference, mode, owner, group, groups, shell, login class, cron user, predicate fact name | a strong secret marks the op sensitive; a weak match is ignored; never refuses |

Only identity fields refuse. Identities are logged and reported on every
host — apply log lines, the `changed ...` summary, errors — so a secret in
one is a leak that marking cannot contain on the destination. Whether a
match refuses depends on the secret's **strength**: a strong secret — at
least `secret.MinStrongLen` (8) bytes after trimming, and not word-like
(more than 12 bytes, or containing something other than ASCII letters, `-`
and `_`, such as a digit) — is evidence of a leak and refuses. A weak one
(`paul`, `root`, `git`, `postgres`, `backup-user`) is too likely an
ordinary account or path name to refuse `/home/paul/.bashrc`,
`User("postgres")` or `WithOwner("postgres")` over; the op is only marked
sensitive. That is the tradeoff: a weak secret that really is in an
identity still reaches the destination's own logs; on the controller every
message redacts it (below). Metadata never refuses, so resolving a secret
can never break an otherwise valid owner or group. The refusal names the op
kind and the field (`command op: its id holds a resolved secret value; this
field is an identity ...`), never the ID or the value; the common case is
an unnamed `Command`, whose ID is its whole argv: give it `WithName`. Task
names, descriptions and host values are not ops and are not scanned; never
put secret values there.

On the controller these outputs pass through the registry:

- log lines (`logger.SetRedactor`), e.g. a `-verbose` registration line or
  a misuse message;
- the apply summary (`changed Command[...]`), CLI error and warning
  messages (every CLI write to stderr), and the push/preview summary lines
  — single-host and cluster/fleet — which all go through one writer
  (`api`'s `pushOutput` seam);
- the output of the processes gonf relays that carry op IDs and values: a
  local elevated apply child (sudo/doas) and a remote gonf over ssh, neither
  of which has the registry. They are redacted line by line
  (`logger.RunRelayed`, a `logger.RedactingWriter`). Every strong line of a
  multi-line secret (a PEM key body line; not the shared `-----BEGIN/END
  ...-----` armour) is a redact-only form of its own, so such a secret is
  hidden line by line too; these line forms are used only to redact output,
  never to mark or refuse an op, because a line such as `[Interface]`, a
  path or a certificate line is no evidence that an op carries the secret.
  An unterminated run longer than 64 KiB is forwarded only up to a point no
  secret can still cross (`secret.MaxSplitGuard`; a secret longer than that
  is redacted only where a forwarded chunk holds it whole).
- When the relayed process has exited (or was killed by its context) but a
  descendant still holds its output — an orphaned root `gonf apply` after
  sudo was killed, under sudo without `use_pty` — gonf returns after at most
  `logger.RelayWaitDelay` (2 s) and keeps draining that output in the
  background, redacted: the descendant is never killed by SIGPIPE, and its
  later lines may appear after gonf moved on. A clean exit stays a success.

Not redacted, because they carry no op IDs or values: flag usage text and
the output of the `scp` and `go build` runs that install the gonf binary.
The destination's own logs, read on the destination (or a remote's system
journal), are limited by the rules above.

The scan recognises a value
verbatim, with surrounding whitespace trimmed and with a trailing newline
trimmed — the transformations recipes actually apply (`strings.TrimSpace`,
`TrimSuffix(s, "\n")`, and `strconv.Quote` of a token without characters
it escapes). So existing recipes need no change: a secret concatenated into
rendered configuration (`WithContent(renderKey(key))`) or placed into
`WithTemplateData` is still found.

For a file whose content is exactly one secret, the typed entry point is:

```go
SecretFile("/etc/goprecords-upload.token", "frontends/fishfinger/goprecords/token",
    WithMode(0o600), WithOwner("root"))
```

It resolves the reference like `MustSecret` (a failure fails the record,
naming the reference only) and manages the file with those exact bytes. Its
mode defaults to `0600` (not `File`'s `0640`); an explicit `WithMode`
overrides it. Options that would replace or reinterpret the content —
`WithContent`, `WithSource`, `WithTemplate`, `WithTemplateData`, a `.tmpl`
path, line edits, `IsAbsent` — are refused as recipe misuse.

Limits of the scan, by design:

- A secret shorter than `secret.MinContainedLen` (4 bytes) after trimming
  is only recognised when a payload value is exactly one of its forms (as
  `SecretFile` content, or a whole argv element, is); searching for 1-3
  bytes inside every payload would mark everything. The rule is decided on
  the trimmed secret, so neither `"123\n"` nor a short secret's JSON
  escaping becomes a substring pattern.
- A multi-line secret (a PEM key, a WireGuard config) marks an op only when
  the op carries it whole; a payload holding only some of its lines is not
  marked, though those lines are still redacted in relayed output and the
  preview.
- A transformation beyond trimming — base64, hashing, splitting, case
  changes — hides the value. Keep such derived material out of plans, or
  resolve the derived form through the provider itself.
- Synced directory trees (`SyncDir`, `Dir` with a source) are not scanned; a
  secret belongs in `SecretFile`/`File`, not in a synced asset tree.
- A secret that is not valid UTF-8, placed as a Go string into template
  data, is recorded with its invalid bytes replaced (`json.Marshal` writes
  U+FFFD), so the recorded value no longer equals the secret and is not
  found; pass binary secrets as `[]byte` (base64, found) or as file content.
- Short secrets in identities: marked, not refused (see above).
- Command argv/environment: a sensitive command's log lines and dry-run
  description show only its binary (`[argv withheld: secret material]`),
  and its failure reports the output sizes, not the output. argv is still
  visible in the destination's process list to every local user, and a
  package manager's own failure output is not withheld. Pass secrets to
  programs through a managed `0600` file instead.
- Content an op writes (a file, a crontab line) is on the destination in
  clear text by design.

### Where a sensitive plan goes

| Output | Behaviour |
|--------|-----------|
| `gonf plan -o dir` | `plan.jsonl` is written `0600` in a `0700`-created, owner-checked directory, as every plan; a secret-bearing plan also gets a stderr warning naming the sensitive ops: it is an executable secret artifact, delete it once applied. A blob-backed secret file's blob lands in `dir/blobs/` with the same protections. |
| `gonf plan -stdout` | Refused, naming the sensitive ops (never their values; `SensitiveOpNames` redacts every resolved secret in the names, a short one an identity equals included). `-stdout -with-secrets` is the explicit export; the operator then owns wherever stdout goes. |
| `gonf plan -redacted` | A human preview on stdout: JSONL headed by a `plan_preview` op, which no gonf version accepts as a plan, with the payload of every sensitive op (content and template data wholesale) and every remembered value in every payload and identity string replaced by `[redacted]`; metadata strings (op kind, owner, mode, ...) only for strong secrets, so a weak secret equal to `file` or `root` does not garble them. Strings are redacted as decoded values and re-encoded, so every line is valid JSON. It is not replayable and must not be labelled as a plan. It cannot be combined with `-stdout`, `-with-secrets` or `-o`. |
| `gonf <task>`, `push`, `cluster`, `fleet` | The plan stays in memory on the controller and travels over SSH stdin (`GONF-PUSH/1`), as before. |
| Destination apply (`gonf apply`) | A failing file (`WithValidation`) or `ConfigSet` validator reports its exit status and only the size of its output ("validator output withheld (N bytes)"), because a validator that quotes the offending line would echo the secret; template parse/execute errors of a sensitive file report the step only. Debug logs never print content digests (for any file: an unsalted sha256 of a low-entropy secret can be confirmed offline). |
| Validation candidates | Unchanged and already private: a file candidate is a `0600` temp file in a parent that only root and the applying user can write; a config set stages below a private staging directory. Both are removed after validation. |

### Transport, privilege and remote versions

- A plan declares schema 22 only when it has a sensitive op
  (`plan.RequiredVersion`); a plan without secret material keeps a v21
  header and still applies with an older gonf (`gonf apply` of a
  `plan.jsonl`). An older remote gonf (plan schema 21, i.e. v0.15.0) cannot
  honour `sensitive` and refuses a v22 plan at its header gate before any
  change. For `push` and strict preview the remote runtime check is
  unchanged: they compare the remote's plan schema and release with the
  controller's own, so `push` installs the controller's gonf first and
  strict preview (`push -preview`) refuses an older remote, whatever the
  plan holds.
- A multi-chunk push with blobs stages every blob in one sticky directory
  owned by the SSH login user. A sensitive op with blob content (a
  secret-bearing file above 512 KiB) in an elevated chunk is therefore
  refused before any SSH traffic: the login user could read it. Push the
  privileged task separately (a single chunk embeds its blobs in the stream
  the elevated apply extracts itself) or keep the content inline.

### Retention and cancellation

- `plan.jsonl` (and `blobs/`) written by `gonf plan -o` stay until the
  operator deletes them. A local run (`gonf <task>`) packages blobs into a
  private per-call temporary directory, and `gonf plan -o` stages them in a
  private `$TMPDIR` directory first; both are removed when the command
  returns or fails, but not when the process is killed by SIGKILL or
  crashes.
- On the destination, embedded blobs are extracted into an owner-only run
  directory removed after the apply; leftovers of killed applies are swept
  after 24 hours. A multi-chunk push's sticky blob directory is removed
  after a successful push only; after a failed or cancelled push it stays
  until the next push of the same plan to that host wipes it.
- Cancelling a push (SIGINT/SIGTERM, a host timeout) kills the in-flight
  `ssh` session; the push payload only ever existed in memory and on that
  stream.
- Secret values and plans live in ordinary Go memory until the process
  exits. Go gives no guarantee that memory is zeroed, and gonf does not
  claim to zero it.

### Not provided

Durable encrypted plans — recipient encryption of `plan.jsonl`, or a
protected sidecar holding only the secret payloads — need their own accepted
design (key management, recipients, what a destination decrypts with) and
are not implied by sensitivity or by any provider. Until then an executable
secret-bearing plan exists only under the private-filesystem protections
above and for as long as the operator keeps it. The foostore adapter is task
162.
