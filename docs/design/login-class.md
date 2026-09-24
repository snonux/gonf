# LoginClass (OpenBSD login classes)

`LoginClass` installs one OpenBSD login class as its own fragment,
`/etc/login.conf.d/<class>`, and returns a change handle for the daemon that
must be restarted to pick up new limits. `NoLoginClass` removes it.

Status: implemented in core (released in v0.15.0). Native OpenBSD verification is
still pending; the platform behaviour below is documentation-based.

```go
func (Web) Inetd() {
    onFrontends(func() {
        flags := File("/etc/rc.conf.local", WithLine("inetd_flags="), WithName("rc-conf-inetd-flags"))
        class := LoginClass("inetd", legacyFrontendAsset("etc/login.conf.d/inetd"))
        config := InstallFile("/etc/inetd.conf", legacyFrontendAsset("etc/inetd.conf"),
            WithMode(0o644), WithOwner("root"), WithGroup("wheel"))
        Service("inetd", WithRestart, OnChange(flags, class, config))
    })
}
```

Signatures:

- `LoginClass(class, src string, opts ...FileOption) Resource`
- `NoLoginClass(class string, opts ...FileOption) Resource` (same as
  `LoginClass(class, "", IsAbsent, opts...)`; no source is needed or read)

## What it records

| Plan op | Behaviour |
|---------|-----------|
| `when_begin` `when.require_goos:openbsd:LoginClass[<class>]` with `goos == openbsd` and `require` (schema 20) | Requirement block around the ops below. On a destination whose GOOS is not `openbsd`, apply **and dry run** refuse the whole plan before anything is written or predicted: `… requirement not met on this host (goos=freebsd): LoginClass[<class>]: only OpenBSD reads per-class fragments … ; nothing was applied` |
| `File[/etc/login.conf.d/<class>.db]` (absent) | Removes a stale compiled database of the fragment, if one exists (see below) |
| `File[/etc/login.conf.d/<class>]` | The owned class content, `root:wheel 0644` by default; with `IsAbsent`, its removal |

`opts` are appended after the defaults, so `WithMode`, `WithOwner`,
`WithGroup`, `WithTemplateData`, `DependsOn`, `WithName`, and even
`WithSource`/`WithContent` (which then replace `src`) refine the fragment.
`WithLine(s)`/`WithoutLine(s)`/`WithKeyedLine` are refused: the fragment is
owned whole.

The returned handle is the two file ops. `OnChange(class)` therefore restarts
the daemon only when the fragment was written, repaired or removed, or when a
stale database was removed; an unchanged apply restarts nothing. Because a
refused plan applies nothing, a refusal never triggers a watcher either.

The requirement block may only sit under host-fact conditions (`goos`,
`profile`, `hostname_contains`, e.g. `WhenHostname`, `ForHosts`, `WhenLinux`,
`WhenProfile`). A `LoginClass` inside `WhenPathExists` is refused when the
plan is recorded, because a filesystem condition can change during an apply
and would make "refused before any mutation" untrue.

A plan containing `LoginClass` needs a destination gonf that supports plan
version 20 or later (older ones refuse the plan up-front).

## Why there is no `cap_mkdb` step

- OpenBSD's `login_getclass(3)` searches `/etc/login.conf.d/<class>` before
  `/etc/login.conf` on every lookup and reads the fragment as text. A changed
  fragment is effective at the next lookup (a running daemon still needs its
  restart to pick up new resource limits).
- `cap_mkdb(1)` compiles only the files named on its command line.
  `cap_mkdb /etc/login.conf` never includes a fragment, so after a fragment
  change it would rebuild `/etc/login.conf.db` from an unchanged
  `/etc/login.conf`. That changes nothing, except that it would activate any
  unreviewed hand edits waiting in `/etc/login.conf`. So LoginClass does not
  run it.
- getcap(3) prefers a compiled `<file>.db` over its text file. A
  `/etc/login.conf.d/<class>.db` can only have been made by someone running
  `cap_mkdb` on this fragment by hand, and it would shadow the managed text
  with stale content. gonf owns the class, so LoginClass removes that file
  (and treats the removal as a class change) instead of refusing to proceed.
  Nothing else is lost: the database is only a compiled copy of this one
  fragment. The alternative, refusing, would need a separate check step and
  a human to delete the same file.

## Registration-time checks

These abort the recipe on the controller before a plan is written:

- The class name must match `[A-Za-z0-9][A-Za-z0-9._-]*` and must not end in
  `.db`: it is used verbatim as a getcap record name and as a file name
  directly under `/etc/login.conf.d`.
- The content that will actually be installed (the caller's
  `WithContent`/`WithSource` if given, otherwise `src`) must contain a record
  named `<class>`, as the canonical name or a `|` alias, in any record of the
  file. OpenBSD consults `/etc/login.conf.d/<class>` only when looking up
  `<class>`, so a fragment without it would never be used. Content that
  cannot be read on the controller is left to the file op to report, and
  names that are still templates (`{{…}}`) are accepted.
- A present class needs content: `src` or `WithContent`/`WithSource`.

Without plan recording (resources registered and applied on the same host),
the OpenBSD requirement is checked immediately against `runtime.GOOS`.

## Per-platform behaviour

Nothing here was run on a BSD. The gonf tests use temp roots, fake facts
(`plan.Facts{GOOS: …}`) and fake command runners. The BSD rows are
**documentation-based**: login.conf(5), login_getclass(3), getcap(3),
cap_mkdb(1), plus what I recall of OpenBSD's libc `login_cap.c` and `getcap.c`.
They still need an authorised live canary.

| Platform | Native behaviour (documentation-based) | LoginClass |
|----------|-----------------------------------------|------------|
| OpenBSD | `login_getclass` searches `/etc/login.conf.d/<class>` before `/etc/login.conf`, so a fragment replaces a same-named `login.conf` class entirely. Its `tc=` references resolve against that fragment and `/etc/login.conf` only, not other fragments (an `inetd` fragment's `tc=daemon` reads the `login.conf` daemon entry) | Supported: installs or removes the fragment; no database rebuild |
| FreeBSD | Classes live only in `/etc/login.conf` (plus per-user `~/.login_conf`), rebuilt with `cap_mkdb /etc/login.conf`; there is no `login.conf.d` | Refused before any write, dry run included |
| NetBSD | Same as FreeBSD | Refused before any write, dry run included |
| Linux | No BSD login classes | Refused before any write, dry run included |

The refusal is tested for all three unsupported platforms with fake facts.
Managing a class inside a shared FreeBSD/NetBSD `/etc/login.conf` would need
keyed block editing of a system-owned file plus a real `cap_mkdb` rebuild.
That is a separate core pattern and deliberately not hidden behind this
resource.

## What it does not do

- It creates no accounts, groups, home or runtime directories. Keep
  `User(..., WithLoginClass(class))` and `Dir(...)` as separate resources and
  order them with `DependsOn(class)` when an account must see its class.
- It does not create `/etc/login.conf.d` (the Rex-managed frontends already
  install fragments there). If the directory is missing, the fragment write
  fails.
- It does not edit `/etc/login.conf` and does not rebuild `/etc/login.conf.db`.
- It does not check that the class *content* is sensible. A fragment replaces
  the whole same-named class, so a `daemon` fragment that only sets
  `openfiles` and `tc=default` drops the stock daemon settings such as
  `ignorenologin`, `datasize` and `maxproc`.
