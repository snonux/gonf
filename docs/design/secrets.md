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
  still live. A nil `*secret.Error` returned as a non-nil `error` (the
  nil-receiver mistake) also becomes `ErrUnavailable`, with a message saying
  so instead of a cause, rather than a panic. An adapter bug is never read
  as "not found".

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
  is a declaration error that keeps the earlier provider and refuses the run
  (see plan.md, "Error handling contract"), so one invocation never mixes
  secret sources.
- A provider implements `Resolve(ctx, secret.Ref) ([]byte, error)`, honours
  `ctx`, returns typed errors and never puts secret bytes into errors or logs.
  Keep adapters (such as the argv-invoked foostore adapter below) out of
  recipe and resource packages; `secret.ProviderFunc` adapts a plain
  function.
- `secret.NewSnapshot(p)` resolves each reference at most once and caches
  successes and not-found; installed with `SetSecretProvider` it lasts for the
  rest of the process, so every task, host and privilege chunk of one
  invocation sees the same value even if the store rotates meanwhile.
  Transient failures are retried. References are cached in canonical form
  (cleaned, without leading slashes or backslashes — `"/a/b"`, `"\a/b"` and
  `"a/b"` share one entry, as `FileProvider` and the pre-provider helpers
  read the same file for all three; a backslash elsewhere, as in `"a\b"`, is
  an ordinary name character). The provider behind a Snapshot is asked for
  the canonical form only (`"a/b"` for all three, `"b"` for `"x/../b"`), so
  it never sees another spelling and cannot cache a not-found that depends
  on one; every error, cached or fresh, names the caller's own spelling in
  its `Ref` and message. A reference with no canonical form (empty, `..`,
  `../x`) is refused as `ErrInvalid` by the Snapshot itself, with the file
  provider's message, so no provider behind it can report it as not-found.
  Build it only with `NewSnapshot`: a zero `secret.Snapshot{}` is
  refused by `SetSecretProvider` like a nil provider, and so is
  `NewSnapshot(nil)`, which returns such a providerless Snapshot (its
  `Resolve` fails with `ErrUnavailable`) instead of panicking. Each reference
  resolves independently: a slow one does not block others, and a caller
  waiting for someone else's resolution of the same reference stops when its
  own context is done. Every returned slice is a copy. Formatting a Snapshot
  (`%v`, `%s`, `%#v`, ...) prints only its provider type and entry count,
  never cached bytes. The default file
  provider is deliberately not wrapped, to keep its read-on-every-call
  behaviour.
- `secret.NewFallback(primary, secondary)` composes two providers for a
  staged, reference-by-reference cutover from one store to another (task
  262): it resolves through `primary` and asks `secondary` only when
  `primary` reports `ErrNotFound` for a well-formed reference — the answer a
  reference not yet listed in `primary`'s own lookup table gives (see
  "Only what the table lists is read" below), so a consumer migrates one
  secret at a time by adding it to `primary`'s table, and every other
  reference keeps reading `secondary` unchanged. Every other failure from
  `primary` — `ErrInvalid`, `ErrUnreadable`, `ErrUnavailable`, a cancelled
  context, or an unclassified error `Resolve` has already turned into
  `ErrUnavailable` — is returned exactly as `primary` reported it, and
  `secondary` is never consulted: a `primary` that is locked, unauthenticated,
  corrupt or otherwise broken for a reference it DOES map fails loudly
  instead of silently serving `secondary`'s possibly stale copy of the same
  secret, which would make that failure indistinguishable from an ordinary
  not-yet-migrated reference. Wrap the whole composition in `NewSnapshot` as
  usual, e.g.
  `secret.NewSnapshot(secret.NewFallback(newProvider, secret.FileProvider{}))`;
  caching then applies to the combined result. `Fallback` holds no state of
  its own and is safe for concurrent use whenever `primary` and `secondary`
  are. Like `NewSnapshot`, `NewFallback` is a composition root: a nil
  `primary` or `secondary` (including a typed nil, e.g. a swallowed
  `foostore.New` error or an unwired feature-flagged provider) never panics.
  `IsNilProvider` recurses into `Fallback` the same way it does into
  `Snapshot`, so `SetSecretProvider` refuses a broken `Fallback` with a
  declaration error at the composition root; `Fallback.Resolve` carries the
  same nil check as a second line of defense and returns a typed
  `ErrUnavailable` instead of crashing if it is ever reached directly.
- `ResolveSecret` may be called from several goroutines; the provider
  configuration is locked. Providers themselves must then be safe for
  concurrent use (`Snapshot`, `FileProvider` and `foostore.Provider` are).
- `ResolveSecret(ctx, ref)` returns the error instead of stashing it and works
  outside recording too. `MustSecret` / `OptionalSecret` resolve with
  `context.Background()`: plan recording carries no context yet.

## The foostore provider (task 162)

Package `github.com/snonux/gonf/secret/foostore` resolves secrets from a
foostore KeePass store by running the `foostore` binary; it does not import
foostore's Go packages. Configure it at the composition root, wrapped in a
`Snapshot`:

```go
items, err := foostore.Items(map[secret.Ref]foostore.Item{
    "garage/rpc_secret": foostore.Field("Infra/garage-rpc", "Password"),
    "nsd/tsig.key":      foostore.Attachment("Infra/nsd/tsig.key"),
})
if err != nil { log.Fatal(err) }
p, err := foostore.New(foostore.Config{Lookup: items})
if err != nil { log.Fatal(err) }
api.SetSecretProvider(secret.NewSnapshot(p))
```

Recipes keep their logical references (`MustSecret("garage/rpc_secret")`);
the table is the explicit mapping from those to foostore items, so a
consumer can switch provider without touching recipe bodies. Only what the
table lists is read: an unmapped reference is `ErrNotFound` (the file
provider's answer for a file that does not exist), so a mistyped required
secret still fails through `MustSecret`. Keys are compared in canonical
form (`secret.CanonicalRef`), as the file provider reads `/a/b` and `a/b`
as one file.

**Contract.** The adapter speaks foostore's machine read contract, called
version 1 here: `foostore read` as added by foostore commit `cd8de3d` (gonf
task y52; foostore README, "Machine-Facing Read"). Each read runs

```text
foostore read --backend keepass --exact --raw --non-interactive \
    --timeout 30s [--kdbx-path P] [--field NAME] -- REFERENCE
```

and returns stdout exactly (binary data, trailing newlines). An entry item
(`Field(reference, name)`) selects one field with `--field`; an attachment
item (`Attachment("Group/Title/name")`) passes no field. Foostore compares
the reference byte for byte (no trimming or path cleaning) and rejects
ambiguous identities. Before its first read a provider runs
`foostore read --help` once and requires the contract's usage text, so an
older foostore whose `read` would be an interactive search is refused
(`ErrUnavailable`) instead of having its search output taken as a secret.
A failed check is retried on the next lookup.

| foostore result | Kind |
|-----------------|------|
| exit 0 | the value (larger than `MaxBytes`, default 16 MiB: `ErrInvalid`) |
| exit 4, not found; or a reference the table does not map | `ErrNotFound` (the only kind `OptionalSecret` suppresses) |
| exit 2, usage (reference not in exact form, a field on an attachment); exit 5, ambiguous | `ErrInvalid` |
| exit 6 locked/credentials, 7 corrupt, 8 store I/O, 1 failure/timeout, any other exit or signal, binary missing, contract check failed, adapter timeout | `ErrUnavailable` |

**No interactive fallback, no leaks.** The child runs with stdin from
`/dev/null`, in a new session without a controlling terminal (so no prompt
can reach an operator's terminal), and with an environment of `HOME` only
(plus the passphrase descriptor below): `FOOSTORE_SHELL`, `PIN` and an
inherited `FOOSTORE_READ_PASSPHRASE_FD` never reach it. argv holds only
logical names — the foostore reference, the field name, the store path and
the timeout. Errors name the gonf reference, the foostore item and the exit
code; foostore's stdout and stderr are never quoted (stderr is reported as a
byte count). A reference with no canonical form (empty, `..`, `../x`) is
`ErrInvalid`, as with the file provider, and never runs foostore.

The adapter overwrites the buffers it owns — a failed read's captured
output, every array its output buffer outgrows, the passphrase — but that is
best effort, not a guarantee: the copy buffer `os/exec` reads the pipe
through, the kernel's pipe buffers and anything the Go runtime has moved
are beyond its reach (see "Retention and cancellation": Go gives no zeroing
guarantee).

**Unlock.** By default foostore unlocks itself with its configured
`kdbx_pass_file` (owner-only). Alternatively `Config.Passphrase` returns the
passphrase for each read (for example from an agent); the adapter writes it
into a pipe the child inherits as descriptor 3
(`FOOSTORE_READ_PASSPHRASE_FD=3`), closes it, and overwrites its copy.
Foostore strips exactly one trailing line terminator (`\n` or `\r\n`) from
what it reads, so a passphrase read from a file may keep its final newline.
The write end is closed once the child has exited even if the passphrase
was not fully written, so a descendant holding the pipe unread cannot block
the lookup. The passphrase is never in argv or the environment. Production unlock material
is outside gonf's tests: they run a fake foostore.

**Operator prerequisites** (controller side, for the default
`kdbx_pass_file` unlock):

- a `foostore` new enough for the read contract above, on the controller's
  `PATH` (or named by `Config.Binary`);
- `~/.config/foostore.json` in the home directory of the user running gonf
  (the child gets only `HOME`; an unreadable or malformed file is foostore
  exit 8, `ErrUnavailable`). It must set `kdbx_pass_file` explicitly:
  foostore's built-in `~/.master.pass` default is never used for machine
  reads, so without it every read is exit 6. `kdbx_path` names the store
  (foostore's default `~/Documents/Keepass/master.kdbx`; `Config.KDBXPath`
  overrides it per provider);
- the pass file itself: a regular file owned by that user with no group or
  other permission bits (`0600`, or `0400`). A missing, lax, foreign-owned
  or empty pass file is foostore exit 6, `ErrUnavailable`, so a mapped
  reference fails the record instead of falling back.

Check the setup without gonf with `foostore read --backend keepass --exact
--raw --non-interactive --field Password -- <reference> >/dev/null; echo $?`
(0 means readable, the exit codes are the table's above; the value goes to
`/dev/null`, never to the terminal).

**Cancellation and time.** `Config.Timeout` (default 30 s) is passed to
foostore as `--timeout`; if the process still runs 2 s after that, the
adapter kills its whole process group (`ErrUnavailable`); a process that
exits on its own while the deadline passes keeps its own result. A cancelled
caller context kills the process group at once and yields an error wrapping
`ctx.Err()`.

**Once per invocation.** Without a `Snapshot` every lookup runs foostore
again (one KDF each); with `secret.NewSnapshot` each reference is read at
most once per gonf invocation, so every task, host and privilege chunk of a
plan sees the same bytes, and a not-found is remembered too. The file
provider stays the default and is unchanged: a consumer that does not call
`SetSecretProvider` keeps reading `secrets/`.

**Staged cutover (example).** To move an existing file-provider consumer to
foostore one secret at a time, put the vault in front of the file provider
with `secret.NewFallback` (see "Configuring another provider" above). This
is conf's `cmd/gonf/main.go` (conf tasks ze2 and its goprecords follow-up),
which maps four references and leaves every other one on `secrets/`:

```go
func setSecretProvider() error {
    items, err := foostore.Items(map[secret.Ref]foostore.Item{
        "frontends/var/nsd/etc/nsd_key.txt":         foostore.Field("Infra/nsd-tsig-key", "Password"),
        "garage/rpc_secret":                         foostore.Field("Infra/garage-rpc", "Password"),
        "frontends/etc/goprecords/blowfish.token":   foostore.Field("Infra/goprecords-token-blowfish", "Password"),
        "frontends/etc/goprecords/fishfinger.token": foostore.Field("Infra/goprecords-token-fishfinger", "Password"),
    })
    if err != nil {
        return err
    }
    vault, err := foostore.New(foostore.Config{Lookup: items})
    if err != nil {
        return err
    }
    api.SetSecretProvider(secret.NewSnapshot(secret.NewFallback(vault, secret.FileProvider{})))
    return nil
}
```

Recipes are unchanged (`MustSecret("garage/rpc_secret")`); migrating one
more secret is one more table row. A mapped reference whose vault read
fails for any reason but not-found (locked store, missing `foostore`) fails
the record instead of reading a possibly stale `secrets/` copy, and a
consumer that has proved the cutover (conf compared every recorded plan
byte for byte with the file-only baseline) can delete the legacy file
copies as a separate step. `main` returns a construction error before
`cli.CLI` runs; the provider is only consulted when a task resolves a
secret, so `-list` and secret-free tasks never run foostore.

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
  `logger.RelayWaitDelay` (2 s) and hands that output to a detached `cat`
  writing to its stderr, in a new session without a controlling terminal
  (so neither Ctrl-C, a hangup nor `stty tostop` stops it). The descendant thus
  keeps a reader for as long as it writes, even after gonf has exited, and
  is never killed by SIGPIPE mid-apply here either: it keeps a reader
  exactly as it had one before gonf relayed its output. What it prints
  after the hand-off (strictly: from the start of the line that was
  unfinished at the hand-off) is **not redacted** (the controller's
  registry dies with gonf); destinations withhold validator output, command
  argv and failure output of sensitive ops themselves, and recording refuses
  strong secrets in identities, so what is left is limited by the rules
  above. A clean exit stays a success. (When stderr is not a file, or `cat`
  cannot start, gonf relays that output redacted in the background, which
  protects the descendant only while gonf runs.)
- The hand-off above only ever runs because gonf itself is still around to
  start it, on its own schedule, once the relayed process has exited or
  been killed by its context. It does nothing for the separate case of the
  CONTROLLER ITSELF dying while relaying a still-running child — SIGKILLed,
  OOM-killed, crashed — since a dead controller runs no hand-off and starts
  no `cat`. The relay pipe's read end simply closes out from under the
  child, local elevated sudo/doas re-exec or the receiving end of a push
  alike; for a plain shell, that write would raise SIGPIPE and kill it,
  exactly what pre-062 avoided by writing straight to the terminal instead
  of a pipe. Both relayed children instead ignore SIGPIPE for their whole
  run (`internal/cli`'s `cliApply`, `ignoreSIGPIPEForRelayedChild`, task
  lb2), so such a write fails with a plain, discarded error and the apply
  keeps going. This is scoped to only a genuine relayed child, not every
  `cliApply` invocation: `cliApply` also runs for an ordinary, non-relayed
  `gonf apply <plan.jsonl>` and a manually piped `gonf apply -`, neither of
  which should have its SIGPIPE disposition touched at all (task lb2's
  original fix ignored it unconditionally there too, which a later review
  found measurably wrong — `gonf apply plan.jsonl` showed SIGPIPE ignored
  in `/proc/self/status` when it should not have been). `cliApply` now
  calls `ignoreSIGPIPEForRelayedChild` only when this process is actually
  marked as a relayed child: `-cancel-pipe` for the local elevated re-exec
  (`api.elevatedApplyArgv`, always set there) or `-relayed` for the
  destination end of a push/preview over ssh (`internal/remote`'s
  `remoteApplyCmd`, also always set there) — task 7d2.

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

### Explicit sensitivity: `WithSensitive`

The scan finds what it can recognise; for everything else a recipe
declares the payload secret itself:

```go
token := strings.TrimSpace(MustSecret("svc/token"))
derived := base64.StdEncoding.EncodeToString([]byte("svc:" + token)) // the scan cannot see this
File("/etc/svc/auth", WithContent("Authorization: Basic "+derived+"\n"), WithSensitive)
SyncDir("/etc/svc/keys", "assets/svc-keys/*", WithSensitive)
Command("/usr/local/bin/register", List("--auth", derived), WithName("register"), WithSensitive)
ConfigSet("svc", ConfigFile("auth", "/etc/svc/auth.conf", WithContent(derived), WithSensitive),
    WithSetValidation("/usr/sbin/svc", []string{"-t", MemberPath("auth")}))
```

- The recorded op is marked sensitive exactly like a detected one, with
  every consequence above and below: schema 22 header, `-stdout` refused,
  `-redacted` withholds every payload string of the op (the preview cannot
  recognise derived material, so it withholds all of it), the elevated-blob
  refusal, and the destination withholding (validator output, template
  details, command argv and output, package-manager failure output).
- It only ever adds sensitivity: an op the scan matched is sensitive
  without it, and nothing else about the op changes, so a recipe without
  `WithSensitive` records byte for byte what it recorded before the option
  existed. It works without any resolved secret too (a secret that came
  from elsewhere).
- It is typed (`options.SensitiveOption`): File, Dir/`SyncDir` (the only
  way to mark a synced tree, which the scan never reads; every copied
  entry is then written as a sensitive file), `ConfigSet` (on the set or on
  any `ConfigFile` member, either marks the whole set, whose one op carries
  every member), Command, Package, Cron and SystemdTimer (whose unit files
  are then written as sensitive files). Link, Service, Timer, DaemonReload
  and User ops carry only identities and metadata, so `WithSensitive` on
  them is a compile-time error, not a silent no-op.
- It hides nothing that is not payload: identities (IDs, paths, names,
  binaries) are still logged, so keep secrets out of them, and content
  still lands on the destination in clear text. A `Command` with
  `WithSensitive` must also have `WithName`: an unnamed command's ID is its
  argv, so the recipe is refused at declaration ("WithSensitive requires
  WithName").

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
  changes — hides the value. Mark such an op with `WithSensitive` (above),
  keep the derived material out of plans, or resolve the derived form
  through the provider itself.
- Synced directory trees (`SyncDir`, `Dir` with a source) are not scanned; a
  secret belongs in `SecretFile`/`File`, or the tree is marked with
  `WithSensitive`.
- A secret that is not valid UTF-8, placed as a Go string into template
  data, is recorded with its invalid bytes replaced (`json.Marshal` writes
  U+FFFD), so the recorded value no longer equals the secret and is not
  found; pass binary secrets as `[]byte` (base64, found) or as file content.
- Short secrets in identities: marked, not refused (see above).
- Command argv/environment: a sensitive command's log lines and dry-run
  description show only its binary (`[argv withheld: secret material]`),
  and its failure reports the output sizes, not the output; a sensitive
  package op's failing package-manager command reports the output sizes
  too, and so does every failing `crontab` run, sensitive or not (one
  table holds every job's lines, so crontab's output may quote another
  job's secret). argv is still
  visible in the destination's process list to every local user. Pass
  secrets to programs through a managed `0600` file instead.
- Content an op writes (a file, a crontab line) is on the destination in
  clear text by design.

### Where a sensitive plan goes

| Output | Behaviour |
|--------|-----------|
| `gonf plan -o dir` | When an operator recipients file exists and `-plaintext` is not given, a secret-bearing plan is sealed by default to `dir/plan.age` instead (task 5b2, approved by the user 2026-09-24; plan-encryption.md "Default seal for sensitive plans"), and an unusable recipients file refuses it rather than falling back to plaintext. Otherwise `plan.jsonl` is written `0600` in a `0700`-created, owner-checked directory, as every plan; a secret-bearing plan also gets a stderr warning naming the sensitive ops: it is an executable secret artifact, delete it once applied. A blob-backed secret file's blob lands in `dir/blobs/` with the same protections. When `dir` sits inside a git worktree that does not already ignore `plan.jsonl` and/or a `blobs/` directory the plan actually wrote, a second stderr warning fires, naming which one(s) (below). |
| `gonf plan -stdout` | Refused, naming the sensitive ops (never their values; `SensitiveOpNames` redacts every resolved secret in the names, a short one an identity equals included). `-stdout -with-secrets` is the explicit export; the operator then owns wherever stdout goes. |
| `gonf plan -redacted` | A human preview on stdout: JSONL headed by a `plan_preview` op, which no gonf version accepts as a plan, with every payload string of every sensitive op replaced wholesale (content, template data, member contents, argv, environment keys and values, lines, cron command and environment, guard and validator arguments, schedules and descriptions; environment keys become numbered `[redacted]-N`, so identities and metadata stay readable) and every remembered value in every payload and identity string replaced by `[redacted]`; metadata strings (op kind, owner, mode, ...) only for strong secrets, so a weak secret equal to `file` or `root` does not garble them. Strings are redacted as decoded values and re-encoded, so every line is valid JSON. It is not replayable and must not be labelled as a plan. It cannot be combined with `-stdout`, `-with-secrets` or `-o`. |
| `gonf plan -o dir -seal [-recipient r]…` | Task 2b2 (docs/design/plan-encryption.md). Records into an in-memory store (never plaintext `plan.jsonl`/`blobs/`), age-encrypts the GONF-PUSH/1 push frame (`plan/seal.Seal`, task 1b2) to the union of `-recipient` flags and the default recipients file, and writes only `dir/plan.age` (`0600`, same directory rules as `plan.jsonl`); `-seal -stdout` writes the sealed bytes to stdout instead, touching no disk. Refused with zero recipients (never a plaintext fallback) and with `-redacted` or `-with-secrets` (sealing and secret-revealing are mutually exclusive concepts). Warns, never deletes, when `dir` also holds a plaintext `plan.jsonl`/`blobs/` left over from an earlier unsealed run. Success is worded "wrote ... (N ops, M recipients)", never "verified" or "trusted": sealing is confidentiality only, never provenance (see plan-encryption.md, "Provenance"). `-seal -sign signer-file` (task 7g2, docs/design/plan-signing.md) signs each sealed artifact in a `GONF-SIGNED-PLAN/1` envelope with a signed-at time; `gonf plan-signer-keygen signer-file` makes the `0600` signer file and prints its public trusted-signers line. The signer secret is never printed, and a refused signer file is never echoed. `gonf apply -trusted-signers f [-require-signed]` (task 8g2) verifies a signed plan and its signed-at freshness (`-max-signed-age`, default 24h) before decrypting it, and `gonf plan-verify` unwraps one for the `age -d` emergency path. |
| `gonf plan -o dir -seal -for host\|cluster\|fleet …` | Task 4b2 (docs/design/plan-encryption.md, "Operator UX" and "Runbook: host keys and shipped plan.age"). Like the row above, but records and seals **once per target host** (`api.RecordPlanForHost`), so a `ForHosts` body written for a host outside that host's `inventory.SelectionForHosts` selection is never resolved while recording another host's plan; writes `dir/plan-<host>.age` per host, each sealed to that host's own `api.WithPlanRecipient` plus the union of `-recipient`/recipients-file. Refuses before writing anything when a resolved host has no recipient (naming it), when the `-recipient`/recipients-file union is empty (same zero-recipient refusal the row above has, task `mg2` — otherwise each artifact would be sealed to its destination host's recipient only, unopenable by the operator who sealed it), or when two resolved hosts' names would sanitize to the same filename. `-for` needs `-seal`; with `-stdout` it needs to resolve to exactly one host. **Not an exact single-host guarantee** (task ng2): the selection is the same substring-based superset `gonf push` itself uses, so a host whose name or SSHHost is a substring of the target's (or vice versa) is included too, and its `ForHosts` secret can physically land in the target's artifact — see plan-encryption.md's "Runbook" for the naming caveat and the two regression tests in `internal/cli/plan_seal_for_test.go` that pin it. |
| `gonf <task>`, `push`, `cluster`, `fleet` | The plan stays in memory on the controller and travels over SSH stdin (`GONF-PUSH/1`), as before. |
| Multi-chunk `push` with a sensitive blob in an elevated chunk | Tasks zf2/0g2 (docs/design/plan-encryption.md, "Phase 4 design"). The controller seals each such blob ref to a fresh per-push ephemeral `age1pq` key held only in memory and uploads only the sealed stream (`sealed/<ref>.age`) to the login user's sticky dir; the key travels only on the reading elevated chunk's own stdin (a `GONF-PUSH/2` frame), never on argv, in the environment, a log or an error. That chunk decrypts every sealed ref its ops read into a fresh `0700` `sealed-run-<pid>-*` directory (dead-owner sweep like a sealed `plan.age` apply) before any op applies, reads those refs only from there, and removes the directory when it returns, failed or not. A missing, tampered, truncated, swapped or over-full sealed ref, a keyed frame with no sealed op, and an op reading a sealed ref without a key are all refused before anything applies, naming ops by position only. Needs a remote gonf at or above the sealed-sticky release floor, v0.17.0 (task yg2): an older remote is refused before any upload. |
| Destination apply (`gonf apply`) | A failing file (`WithValidation`) or `ConfigSet` validator reports its exit status and only the size of its output ("validator output withheld (N bytes)"), because a validator that quotes the offending line would echo the secret; template parse/execute errors of a sensitive file (or of an entry of a sensitive synced tree) report the step only; a failing command or package-manager run of a sensitive op, and every failing `crontab` run, reports only its output sizes. Debug logs never print content digests (for any file: an unsalted sha256 of a low-entropy secret can be confirmed offline). |
| Validation candidates | Unchanged and already private: a file candidate is a `0600` temp file in a parent that only root and the applying user can write; a config set stages below a private staging directory. Both are removed after validation. |

**Git-worktree warning (task 0b2, phase 0 of the plan-encryption design,
docs/design/plan-encryption.md; extended to cover `blobs/` by task zd2).**
`plan.jsonl` written by `gonf plan -o dir` (without `-seal`) stays
plaintext, and the default `-o .` is normally the recipe checkout, which
backups, sync tools and an operator's own `git add -A` all read. A
`WithSensitive` payload larger than
`plan.MaxInlineContent` is written as plaintext too, but not into
`plan.jsonl` — it lands in `dir/blobs/<name>-<hash>` instead. After writing
a secret-bearing plan, `gonf plan -o dir` therefore checks whether `dir`
sits inside a git worktree and, if so, whether `plan.jsonl`, and separately
`blobs/` when the plan actually wrote one, are already covered by that
worktree's ignore rules (`internal/cli`'s
`warnIfPlanUnignoredInGitWorktree`, `git_worktree_warn.go`). Both paths are
checked, and independently: a repository that ignores `plan.jsonl` but not
`blobs/` is not a pass — it is the worse case in practice, since
`plan.jsonl` then carries no secret bytes at all while `blobs/` still holds
cleartext, and it is exactly the state an operator following this warning's
first suggested fix (ignore `plan.jsonl`) ends up in.

- Detection never touches or trusts the repository's own configuration for
  the risky part: first a plain filesystem walk up from `dir` for a `.git`
  entry (a directory or a worktree's `gitdir:` file), with no git process
  started at all when none is found (the common case for a private, non-git
  `-o` directory); only once an ancestor is found does it run `git
  check-ignore -q <path>` once per candidate path — `plan.jsonl` always,
  `blobs` only when `dir/blobs` actually exists — with `dir` as the working
  directory, through `internal/exec` with a short timeout separate from
  gonf's usual, much longer command-timeout default. Each path gets its own
  `git check-ignore` call rather than one call naming both, because git's
  `-q` refuses more than one pathname at once ("--quiet is only valid with a
  single pathname"); checking one at a time also lets the warning name
  exactly which path is the problem.
- The check is warn-only and fails safe per path: it can never refuse or
  delay the plan write (already on disk by the time it runs), and anything
  that keeps it from getting a clear answer for a given path — no git
  binary, a timeout, an unexpected git exit status — is treated as "cannot
  tell" for that path and stays silent for it, never a false warning.
- When `plan.jsonl` and/or a written `blobs/` is not ignored, the warning
  names `dir` and which of the two paths are unignored, and suggests adding
  a `.gitignore` entry for `plan.jsonl` (and `blobs/`), writing the plan to
  a private `-o <dir>` outside any checkout, or using `gonf plan -o <dir>
  -seal` (task `2b2`) to write an encrypted `plan.age` instead — safe to
  keep in a git worktree, a backup or a CI artifact store even unignored.
- Like the secret-artifact warning above, this goes through the CLI's
  redacting stderr path (`eprintf`, `logger.Redact`), though the message
  itself carries only the output directory path and which of `plan.jsonl` /
  `blobs` are unignored, never plan content.
- A plan without secret material is completely unaffected: the git-worktree
  check runs only after the secret-artifact warning already found sensitive
  ops, so a plain plan never even attempts the `.git` walk. A plan whose
  only sensitive material stayed inline (no blob written) only ever probes
  `plan.jsonl`, exactly as before zd2.

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
  owned by the SSH login user. The blob of a sensitive op with blob content
  (a secret-bearing file above 512 KiB) in an elevated chunk therefore never
  lands there as plaintext: it is sealed to a per-push ephemeral key and
  decrypted only by that elevated chunk, into its own private run
  directory (w82 phase 4, tasks zf2/0g2; this replaced 062's refusal of
  such plans, see "Where a sensitive plan goes" below). Until the release
  carrying task 0g2 is tagged, the controller's sealed-sticky release floor
  refuses every remote, so such a push still fails closed before any
  upload; pushing the privileged task separately, or keeping its content
  inline, avoids the sticky dir altogether.

### Retention and cancellation

- `plan.jsonl` (and `blobs/`) written by `gonf plan -o` stay until the
  operator deletes them. A local run (`gonf <task>`) packages blobs into a
  private per-call temporary directory, and `gonf plan -o` stages them in a
  private `$TMPDIR` directory first; both are removed when the command
  returns or fails, but not when the process is killed by SIGKILL or
  crashes.
- `dir/plan.age` written by `gonf plan -o dir -seal` (task 2b2) likewise
  stays until the operator deletes it; sealing changes confidentiality at
  rest, not retention. It has no forward secrecy (age has none): whoever
  later obtains a recipient's private identity can decrypt every
  `plan.age` ever sealed to it that still exists anywhere, so keys should
  be rotated periodically (see docs/design/plan-encryption.md, "No forward
  secrecy; rotate").
- On the destination, embedded blobs are extracted into an owner-only run
  directory removed after the apply; leftovers of killed applies are swept
  after 24 hours. A multi-chunk push's sticky blob directory is removed
  after a successful push only; after a failed or cancelled push it stays
  until the next push of the same plan to that host wipes it (it then holds
  only the sealed form of a sensitive elevated blob, never its plaintext).
  An elevated chunk's private `sealed-run-*` directory with the decrypted
  copies is removed when the chunk returns; a killed chunk's leftover is
  swept by the next sealed apply of that uid once its PID is dead.
- Cancelling a push (SIGINT/SIGTERM, a host timeout) kills the in-flight
  `ssh` session; the push payload only ever existed in memory and on that
  stream.
- Secret values and plans live in ordinary Go memory until the process
  exits. Go gives no guarantee that memory is zeroed, and gonf does not
  claim to zero it.

### Not provided

Durable encrypted plans now have an accepted design
(docs/design/plan-encryption.md, task w82), implemented on both
sides: `gonf plan -o dir -seal` (task 2b2, this document's "Where a
sensitive plan goes" table) age-encrypts the GONF-PUSH/1 frame to
operator-controlled `age1pq…` recipients and writes only `dir/plan.age`,
and `gonf apply -identity file... <plan.age|->` (task 3b2) decrypts and
applies it with the same single-process, file-apply semantics as a
plaintext plan. Since task 5b2 (approved by the user 2026-09-24), `gonf plan -o dir`
seals a secret-bearing plan by default once an operator recipients file
exists (`-plaintext` opts out); without such a file, sealing still needs an
explicit `-seal` with at least one recipient. Either way it is
confidentiality only, never provenance (plan-encryption.md,
"Provenance"): `gonf apply` of a sealed plan never prints "verified" or
"authenticated", and nothing in gonf may apply a sealed plan unattended
until an entry point meets plan-signing.md's "The unblocking condition".
Signing itself exists (`gonf plan -seal -sign`, `gonf plan-signer-keygen`,
and `gonf apply -trusted-signers [-require-signed]` verifying before it
decrypts, tasks `6g2`/`7g2`/`8g2`), but only for an operator-chosen file:
the unattended entry point (signing phase 6, task `bg2`) was declined and
is not planned. A protected sidecar holding only
the secret payloads (rather than sealing the whole artifact) was
considered and rejected — plan-encryption.md's "Options compared", option
E. Per-destination sealed artifacts now exist too: `gonf plan -o dir -seal
-for host|cluster|fleet` (task 4b2, this document's "Where a sensitive plan
goes" table and plan-encryption.md's "Runbook") seals one `plan-<host>.age`
per destination, each to that host's own `api.WithPlanRecipient`. Not
provided: resolving the operator identity through a secret provider (task
5b2's second option, declined by the user on 2026-09-24) — see
plan-encryption.md's "Phased implementation" for the full list. Without a
recipients file, `-seal` or `-identity`, nothing here changes: `gonf plan -o
dir` still writes a plaintext `plan.jsonl` under the private-filesystem
protections above, for as long as the operator keeps it. The foostore provider (above)
changes where secrets come from, not what the plan holds either way.
