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
| `secret.ErrUnavailable` | the store itself is unusable | the `secrets/` directory itself is missing; misconfigured `Dir`; for other providers: locked, unauthenticated, corrupt, crashed |

Decide optional lookups with `secret.IsNotFound(err)` (`KindOf` reads only
the outermost `*secret.Error`, so a cause further down cannot make a broken
store look like an absent secret). Plain `errors.Is(err, secret.ErrNotFound)`
also traverses into causes; it is for matching and logging, not for the
suppress decision.
A cancelled or expired context yields an error wrapping `ctx.Err()` and no
`*secret.Error`. `secret.Resolve` wraps every provider call: it refuses to
start on a done context, drops bytes a provider returns after cancellation,
and turns any error that is not a correctly typed `*secret.Error` into
`ErrUnavailable` — an adapter bug is never read as "not found".

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
All other messages are unchanged.

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
- `secret.NewSnapshot(p)` resolves each reference at most once per process
  and caches successes and not-found, so every task, host and privilege chunk
  of one invocation sees the same value even if the store rotates meanwhile.
  Transient failures are retried. The default file provider is deliberately
  not wrapped, to keep its read-on-every-call behaviour.
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
