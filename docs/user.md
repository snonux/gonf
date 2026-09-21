# User resource

`User` ensures a local account exists on Rocky Linux, OpenBSD, FreeBSD, or
NetBSD. It is intentionally additive by default: gonf never deletes an account
or a group, removes a supplementary membership, or rewrites an existing
account's primary group, shell, login class, or system-account setting. An
existing account's home field is rewritten only when the recipe explicitly
opts in with `WithManageHome` (see below).

```go
User("_svc",
    WithPrimaryGroup("_svc"),
    WithUserGroup("wheel"),
    WithSupplementaryGroups("audio", "video"),
    WithHome("/var/svc"),
    WithShell("/sbin/nologin"),
)
```

`WithPrimaryGroup` selects the primary group when creating a missing account.
`WithUserGroup` adds one supplementary membership; `WithSupplementaryGroups`
adds several. Existing supplementary memberships not requested by the recipe
are retained.

`WithHome`, `WithCreateHome`, `WithShell`, `WithLoginClass`, and `WithSystem`
are creation-time settings. `WithHome` alone does not create a directory; add
`WithCreateHome` to create the configured (or platform-default) home directory.
Rocky Linux rejects login classes. The BSD backends reject `WithSystem`, since
their portable user-management mode has no supported system-account flag.
Linux system accounts and BSD login classes are different concepts; gonf never
translates one into the other.

`WithGroup` remains accepted as a compatibility spelling for the creation-time
primary group, and `WithClass` aliases `WithLoginClass`; prefer the explicit
user option names above in new recipes.

## Per-platform creation behavior

Two kinds of statements follow. **Verified in gonf's unit tests** means the
exact command sequence gonf issues, checked against scripted command runners.
**Per platform documentation** describes what the platform tool then does;
it comes from the tools' manual pages and stock configuration and has **not**
been run natively by gonf's test suite on any of these systems.

Commands issued for a missing account (verified in gonf's unit tests):

| | Rocky Linux | OpenBSD / NetBSD | FreeBSD |
|---|---|---|---|
| Missing requested groups (primary and supplementary) | `groupadd -- G` each, before `useradd` | `groupadd G` each, before `useradd` | `pw groupadd -n G` each, before `pw useradd` |
| No `WithPrimaryGroup` | no `--gid`: the tool picks the group | no `-g`: the tool picks the group | gonf passes `-g NAME` and creates group `NAME` if missing |
| Account creation | `useradd [--create-home\|--no-create-home] [--system] [--gid] [--groups] [--home] [--shell] -- NAME` | `useradd [-m] [-g] [-G] [-d] [-s] [-L] NAME` | `pw useradd -n NAME [-m] -g G [-G] [-d] [-s] [-L]` |
| `WithLoginClass` | rejected before any command | `-L CLASS` | `-L CLASS` |
| `WithSystem` | `--system` | rejected after the account probe, before any mutation | rejected after the account probe, before any mutation |
| More than 16 supplementary groups | allowed | rejected before any command | allowed |

What the tools then do (per platform documentation, not verified natively):

- **Primary group when none is requested.** Rocky's `useradd` creates a
  same-named private group when `USERGROUPS_ENAB yes` is set in
  `/etc/login.defs` (the stock setting). OpenBSD and NetBSD `useradd` use the
  `group` default from `/etc/usermgmt.conf` (see `useradd(8)` and
  `usermgmt.conf(5)` on the target release). Declare `WithPrimaryGroup` when
  the primary group matters, so gonf creates it explicitly on every platform.
- **Mode of a home created with `WithCreateHome`.**
  - Rocky Linux: `HOME_MODE` from `/etc/login.defs`, or `0777 & ~UMASK` from
    the same file when `HOME_MODE` is unset. The stock Rocky 9 file sets
    `HOME_MODE 0700`.
  - FreeBSD: the `pw useradd -M` mode or `homemode` in `/etc/pw.conf`. When
    neither is set, the mode comes from the umask of the elevated gonf process.
  - OpenBSD: `useradd -m` creates the directory with the umask of the
    elevated gonf process applied, copies the skeleton directory (`/etc/skel`
    by default, configurable in `/etc/usermgmt.conf`), and chowns it to the
    new account.
  - NetBSD: `useradd -m` (built with its default extensions) copies the
    skeleton directory, chowns the home to the new account, and then chmods
    it to `homeperm`. That comes from `useradd -M` or `/etc/usermgmt.conf`
    and defaults to `0755`, whatever the umask. gonf passes no `-M`.
  - On every platform, declare a `Dir(home, WithOwner(...), WithMode(...),
    DependsOn(account))` when the exact mode matters. `WithCreateHome` never
    applies to an account that already exists.
- **Passwords and non-login accounts.** gonf never sets or changes a password.
  Rocky's `useradd` leaves a new account's shadow password locked (`!!`), and
  `pw useradd` and the OpenBSD/NetBSD `useradd` default to a disabled `*`
  password unless configured otherwise. gonf has no separate "non-login"
  switch. Use `WithShell` with the platform's nologin shell (`/sbin/nologin`
  on Rocky, OpenBSD, and NetBSD; `/usr/sbin/nologin` on FreeBSD). On the BSDs,
  add `WithLoginClass` for a class that must already exist in `login.conf`.

## Managing an existing account's home field

`WithManageHome` opts one `User` in to converging the passwd home field of an
account that already exists to its `WithHome` value:

```go
account := User("_svc",
    WithPrimaryGroup("_svc"),
    WithHome("/var/svc"),
    WithManageHome,
)
Dir("/var/svc", WithOwner("_svc"), WithGroup("_svc"), WithMode(0o755), DependsOn(account))
```

It replaces the guarded `Command("usermod", List("-d", …))` plus
`awk … /etc/passwd` workaround. The contract is deliberately narrow:

- **Only the passwd home field changes.** gonf reads the account record,
  compares the recorded home with `WithHome`, and runs a single field update
  only when they differ. A matching field is a no-op and reports `ok`.
- **No data moves.** The update never passes the move flag (`-m` /
  `--move-home`): it does not move, copy, create, delete, or recursively chown
  the old or new directory. Declare a `Dir` resource (as above) when the
  directory itself must exist with a particular owner and mode.
  `WithCreateHome` still only applies while creating a missing account.
- **Nothing else is rewritten.** Passwords, locked (`*`, `*LOCKED*`, `!`)
  and non-login accounts, the shell, the login class, the primary group, and
  supplementary memberships are left exactly as they are. Memberships are
  still only added, and they are added before the home update.
- **Missing accounts** are created exactly as without the option: `WithHome`
  is passed to the creation command, and no separate update follows.
- **Validation.** `usermod` and `pw` store the home exactly as given, so
  `WithManageHome` accepts only canonical, unambiguous values that the passwd
  format can hold. `WithHome` must be set, absolute, and clean (no trailing
  `/`, `.` or `..` segments, or repeated `/`), so each directory has exactly
  one spelling in the passwd database. It must also contain no `:` (the field
  separator), line break (the record separator), or NUL. A violation fails
  `gonf plan` at record time and is checked again before any destination
  command runs.
- **Wrong-account lookups.** A probe that returns another account's entry is
  an error, never a reason to update. glibc `getent passwd` resolves an
  all-digit name as a UID, for example. The error names the account that was
  returned.
- **Opt-in only.** Without `WithManageHome`, `WithHome` stays a creation-time
  attribute. Recipes that do not opt in record the same plan operation and
  run the same commands as before.

Per-platform commands (probe, then field update):

| Platform | Probe | Update |
|----------|-------|--------|
| Rocky Linux | `getent passwd NAME` (6th field) | `usermod --home HOME -- NAME` |
| OpenBSD | `getent passwd NAME` (6th field) | `usermod -d HOME NAME` |
| NetBSD | `getent passwd NAME` (6th field) | `usermod -d HOME NAME` |
| FreeBSD | `pw usershow -n NAME` (9th, master.passwd field) | `pw usermod -n NAME -d HOME` |

According to the platform documentation, these updates (without the move
flag) change only the passwd field. On Rocky Linux, shadow-utils `usermod`
may refuse to change the home of an account that has running processes. gonf
returns that error rather than stopping anything. Dry runs probe the account
and report the update as a would-change without running it.

These command sequences are verified against scripted command runners in the
unit tests; they are not a substitute for native validation on each OS.

### Homes under `/var/run`

OpenBSD clears `/var/run` at boot, and other platforms may keep it on a
memory file system. A `Dir` for a home under `/var/run` recreates the
directory only when gonf next applies, not at boot. `WithManageHome` only
keeps the passwd field pointing there. Services whose home lives under
`/var/run` need their own boot-time lifecycle, such as an `rc.local` line or
the service's own startup script. Prefer a persistent location such as
`/var/<service>` when the choice is free.

## Plan wire

The resource is carried through the plan schema as a `user` operation, so the
same behavior applies to local `Apply`, recorded plans, and remote push/apply.
`WithManageHome` is recorded as `manage_home` (plan schema version 19). Older
gonf binaries refuse v19 plans at the header gate instead of silently skipping
the home update.

See also: [options.md](options.md), [plan.md](plan.md), and the
[documentation index](README.md).
