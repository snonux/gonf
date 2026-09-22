# Secrets and secret providers

gonf resolves secrets on the **controller**, while a task body is recorded.
Recipes read them with `MustSecret` / `OptionalSecret` (strings, the
long-standing helpers) or `ResolveSecret` (bytes plus a typed error). All
three go through one **secret provider**, configured once per process.

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

## What reaches the plan

The provider contract resolves bytes only; it does **not** make plans
secret-aware. Resolving a secret records nothing. A value a recipe places
into file content (`WithContent`) or template data is recorded exactly as
before: in clear text in the owner-only (`0600`) `plan.jsonl` and in the SSH
transport payload. Do not use `gonf plan -stdout` for such recipes, and never
put secret values into task names, descriptions or host values. Carrying
sensitivity through plans, previews, validators and transport (typed secret
references in file content and template data, redacted previews) is separate
follow-up work (task 062), as is a foostore adapter (task 162).
