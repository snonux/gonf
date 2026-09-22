# ConfigSet

`ConfigSet` manages several configuration files that are only valid together:
a daemon config plus its lookup tables, an include, or a key file. The whole
candidate set is staged and validated **before any live file changes**, and
every member gets its own change handle, so a gate can react to one member
instead of to the whole set.

```go
aliases := assets("etc/mail/aliases")
mail := ConfigSet("smtpd",
    ConfigFile("aliases", "/etc/mail/aliases", WithSource(aliases), WithMode(0o644)),
    ConfigFile("virtualusers", "/etc/mail/virtualusers", WithSource(users), WithMode(0o644)),
    ConfigFile("smtpd.conf", "/etc/mail/smtpd.conf", WithContent(conf), WithMode(0o644)),
    WithSetValidation("smtpd", List("-n", "-f", MemberPath("smtpd.conf"))))

newaliases := Command("newaliases", List(), OnChange(mail.Member("aliases")))
Service("smtpd", WithRestart, DependsOn(newaliases),
    OnChange(mail.Members("virtualusers", "smtpd.conf")...))
```

`conf` refers to its tables through placeholders, never through literal paths:

```go
conf := "table aliases file:" + MemberPath("aliases") + "\n" + ...
```

## API

| Name | Meaning |
|------|---------|
| `ConfigSet(name, opts...)` | Registers `ConfigSet[name]` plus one `ConfigSetMember[name/key]` handle per member and returns a `ConfigSetResource`. |
| `ConfigFile(key, path, fileOpts...)` | One member at an absolute, clean live path. Content from `WithContent` or `WithSource`; `WithMode` (default `0640`, as `File`), `WithOwner`, `WithGroup`. Other file options are refused. |
| `WithSetValidation(bin, args)` | An argv validator (no shell). At least one is required; they run in order and must all exit 0. |
| `MemberPath(key)` | Typed placeholder for a member's absolute path: the staged candidate while validating, the live path when published. Usable in member content and validator arguments. |
| `MemberChrootPath(key)` | The same path relative to `WithChroot` (with a leading `/`), for configuration read after `chroot(2)`. |
| `WithChroot(dir)` | Every member and the staging directory must lie below `dir`. |
| `WithStagingDir(dir)` | Where the private staging directory is created; default is the members' deepest common directory. Must be an ancestor of every member (and inside the chroot, if any). |
| `DependsOn(...)` | Ordinary dependencies of the set. |
| `set.Member(key)` / `set.Members(keys...)` | Member handles for `OnChange`/`DependsOn`; `Members()` with no keys returns all. An unknown key fails the record. |

The set itself is a resource too: `OnChange(set)` fires when any member was
published. Watch the set or its member handles, not the directory: an
`OnChange(Dir(...))` gate (`Directory[path]`) only sees `File[...]` changes
below that path, and a published member is reported as
`ConfigSetMember[set/key]`, not as a `File`, so such a gate never fires for
it. Keys and set names use letters, digits, `.`, `_` and `-`.

`WithSource` is read once on the controller at record time, with the same
rules as `File`: a FIFO, socket, device or directory is refused. Templates
are **not** rendered: a `.tmpl` source or member path is refused at record
time, because the member would otherwise be published as raw template text.
Render the content in the recipe and pass it with `WithContent`.

Placeholders contain NUL bytes, like `CandidatePath`, so they cannot collide
with real configuration text. Gonf rejects malformed or unknown placeholders at
record time and again before apply. The only transformation applied on the
destination is replacing the placeholders; all other bytes are what the
controller rendered.

## What happens on apply

1. **Prepare.** Validate the set description, render the live bytes of each
   member, and (on a real apply) resolve every owner and group, so an unknown
   account fails the whole set before anything is staged or published.
2. **Lock.** Open the parent directory of every member without following any
   symlink (a symlink anywhere in the path is refused), drop duplicates by
   device and inode, and take an exclusive `flock(2)` on each in (device,
   inode) order. Waiting for a concurrent publication is
   bounded (5 minutes); after that the apply fails with a timeout error.
3. **Diff.** A member differs when its path is missing or holds other bytes.
   Members are opened without following symlinks and without blocking on a
   FIFO, and compared from the opened file. A
   symlink, FIFO, socket, device or directory at a member path is refused
   (the `File` resource would replace it; a set does not overrule that
   operator decision). Also look for the members' pending markers (see
   below). If nothing differs, repair attributes and go to step 7.
4. **Stage and validate.** Check the staging directory with the same rules as
   `WithValidation` candidate parents (see [file-dir-link.md](file-dir-link.md)
   for the full rule). Every component must be a real directory. Ancestors
   must be owned by root or the applying user, and may be writable by others
   only when sticky. The staging directory itself must be owned by the
   applying user, not world-writable, and group-writable only when its group
   is the applying user's private group (as with the `0775` directories the
   umask-002 default of Fedora and most Linux distributions creates; gid `0`
   is never private, so a root apply gets no such exception). Inside it,
   create a private `.gonf-configset-<name>+XXXX` directory (0700)
   and write the **complete** set there, unchanged members included, as 0600
   files (created exclusively) that mirror the members' layout. So relative
   references between members resolve the same way, and with `WithChroot` the
   candidates are inside the chroot. Every validator runs with placeholders
   rendered as staged paths and with the staged mirror as its working
   directory. If any validator fails, the staging directory is removed, **no
   live file is touched**, nothing is reported as changed, and no gate fires.
   Validators run exactly like `File`'s `WithValidation` (the same runner):
   without a shell, with stdin from `/dev/null`, bounded by the per-command
   timeout (`-cmd-timeout` / `api.SetCommandTimeout`, 5 minutes by default;
   on expiry the validator process is killed and validation fails with
   `... timed out after 5m0s: context deadline exceeded`), and a failure
   carries the validator's combined stdout/stderr with the same output rules
   (sanitized, lines joined by ` | `, at most 4 KiB, truncation note); see
   [file-dir-link.md](file-dir-link.md). For example:
   `config set smtpd: validation by smtpd failed, nothing published: exit
   status 1: validator output: ...`. Whatever a validator prints is reported,
   so do not use one that echoes secret member content.
5. **Repair unchanged members.** Re-apply mode and ownership to the members
   that are not about to be replaced. This happens before any live rename, so
   a failure here publishes nothing. As with `File`, a metadata-only repair is
   not reported as a change.
6. **Publish.** Back up every member about to be replaced into `backups/`
   inside the staging directory: a hard link to the old inode or, if that is
   refused, a copy (created exclusively, fsynced) with the old bytes, mode and
   numeric owner. Then, member by member in declaration order, create the
   member's pending marker and replace the member with the `File` write path:
   private temp file beside the target, fsync, rename, directory fsync, then
   `O_NOFOLLOW` chown/chmod.
7. **Report.** Note `ConfigSet[name]`, which changes when any member was
   published, and each `ConfigSetMember[name/key]`, which changes only when
   that member was published or has a pending marker (logged as "signalling
   the pending change of ..."). Then remove those members' markers, fsyncing
   each directory. The change is already recorded at that point, so a marker
   that cannot be removed only produces a warning: if the unlink failed, the
   marker stays and the next apply signals the member once more; if only the
   directory fsync after the unlink failed, the marker is gone and a later
   apply may signal once more only if a power loss brings it back.

Validation runs whenever something is to be published, including when only
the live file drifted (someone edited it) while the recorded content stayed
the same. A replay with nothing to publish does not run the validators.

Under dry-run, the owner and group lookup, locking, staging, validation and
publication are skipped. The set and its members report `would-change` from
the diff and the pending markers alone (the markers are kept), so **a dry-run
cannot prove the set is valid**, nor that its owners and groups exist. Like a
`File`, a set owned by an account that a `User` or `Group` earlier in the same
recipe creates therefore previews cleanly on a host that lacks the account;
the real apply creates the account first and then resolves it.

### Pending markers

Every member has its own pending marker: a small file in the member's own
directory named `.gonf-pending.` followed by 24 hex digits of a hash of the
set name, the member key and the member path. It is created right before that
member's live rename (exclusive, without following a symlink, 0600, fsync of
the file and of the directory) while the apply holds the member directory
locks. If writing it or fsyncing its directory fails, it is normally unlinked
again and nothing is published; only if that unlink fails too (the error says
so) can a later apply signal the member once. It is removed (unlink, directory
fsync) only after the apply has reported the member. An apply of a set with the same name and a member with
the same key at the same path finds the marker and reports that member as
changed even when its live content already matches, so its `OnChange` gates
(restart, `newaliases`) fire. The marker is opened without following a
symlink and must be a regular file owned by the applying user; anything else
fails the apply. Its content (set, member, path as JSON) is for operators
only.

Because the name covers set, key and path, two recipes that both define a set
called `nsd` never consume each other's markers unless they manage the very
same file. Markers need no state directory and do not depend on `$HOME`,
`$XDG_STATE_HOME` or any other environment. A marker whose member later moves,
is renamed or leaves the set is orphaned; that is harmless (the member at its
new path is published and signalled anyway) and can be deleted by hand.

**What this guarantees** (on filesystems that support directory fsync): a
member's change signal survives a crash, kill, power loss or failed apply
between that member's rename and the end of its set's apply; the next apply
signals it. **What it does not**: the marker is
removed right after the set reports the change, which is before any gated
`Command` or `Service` runs. A crash after that point, or a failing gated
action, loses the signal, because change reports are process-local. Failures
of the marker handling itself over-signal, never under-signal: a member is
signalled one more time when its marker could not be unlinked after reporting
(a warning) or by a rollback that restored it, and may be signalled once more
when only the directory fsync after such an unlink failed (the marker then
only comes back after a power loss). A rollback that restores a member whose
earlier publication was still pending keeps that marker, which signals once
more too.

The directory fsyncs of the marker handling and of restores are stricter than
the `File` resource, whose atomic write ignores every directory fsync error:
only a filesystem that does not support fsync of a directory at all (`EINVAL`,
`ENOTSUP` or `EOPNOTSUPP`, as FUSE or 9p may report) is treated as
best-effort and the apply continues without that durability; any other fsync
error fails the apply. The directory fsync after a member's own rename is done
by the `File` write path and follows its ignore-all rule.

Pruning directory syncs (`SyncDir`/`Dir` with `WithPrune`) do not know about
config sets. A pruned tree sync (`WithSource` directory) over a directory that
also holds config-set members deletes `.gonf-pending.*` markers, so their
signals are lost, and it deletes leftover `.gonf-configset-<name>+*` staging
directories, which after `ROLLBACK INCOMPLETE` hold the only backups; it
reports those removals as its own changes. A pruned glob sync deletes markers
but skips directories. Do not prune a directory that holds config-set members
or is a config set's staging directory.

## Guarantees and limits

Several renames are **not** an atomic transaction. Gonf guarantees this much:

- **Failed validation or failed attribute repair**: live contents are exactly
  as before. Nothing is reported as changed and no gate fires.
- **A replacement fails** (write, rename, chown or chmod): Gonf rolls back
  every member it replaced in this apply, including the one that failed, in
  reverse order. A member backed up by hard link gets back its original inode
  (bytes, mode, owner, every inode attribute). A member backed up by copy gets
  back its bytes, permission bits and numeric owner, but not ACLs, extended
  attributes or timestamps. A member that did not exist before is removed. Each
  restored member's directory is fsynced **before** the marker this apply
  created for it is removed (a marker from an earlier, unsignalled
  publication stays), so (on filesystems that support directory fsync) a
  power loss cannot keep a restored-away new file while the marker that would
  signal it is gone. The
  apply then fails with an error that says the rollback completed, and
  nothing is reported as changed. If a member was restored durably but only
  its marker could not be removed, the rollback still counts as complete (the
  staging directory is removed); the error names that member and says
  whether the next apply signals it once more (the unlink failed) or may
  (only the directory fsync after the unlink failed).
- **A restore (or making it durable) fails too**: the error says
  `ROLLBACK INCOMPLETE`, names every
  member left in an unknown state, and keeps the staging directory. Its
  `backups/` holds a hard link or an on-disk copy of every replaced member, so
  an operator can restore by hand. The markers of those members stay, so the
  next apply signals them.
- **Crash, kill or power loss between two renames**: no rollback runs. The
  live set can be a mix of old and new members, and the staging directory
  (with its backups) is left behind. The next apply takes the locks, warns
  about the leftover directory (it is never reused or deleted automatically),
  diffs, **revalidates the complete set**, converges forward, and, through the
  pending markers, reports every member the interrupted apply renamed as
  changed, so their gates fire. Replaying the same plan is always safe.
- **Signals are process-local once reported**: a member is reported during
  the apply that signals it and its marker is removed then. If that apply
  crashes afterwards or a gated restart fails, the next apply does not
  re-signal.
- **Readers in between**: a daemon that reloads on its own while the renames
  are in progress can see a mix of old and new members. Every single file is
  always either complete-old or complete-new. Gonf only restarts or reloads
  through gates, and those run after the whole set is published.
- **Serialization**: two ConfigSet applies whose members share a directory
  (two gonf processes, or concurrent applies in one process) are serialized
  for their whole diff-validate-publish-report sequence, including the
  marker updates in that directory. Sets without a shared directory do not
  wait for each other. Waiting is bounded by the 5-minute timeout. The locks
  are advisory: plain `File` resources, package scripts and editors do not
  take them.
- **Local filesystems only**: the locks are `flock(2)` on the member
  directories, which Linux NFS does not support on a directory. Member
  directories on NFS (or any filesystem refusing it) are unsupported; the
  apply fails before any write with an error saying so. `ENOLCK` (no locks
  available right now) is reported separately as a transient error to retry.
- **Chroots**: gonf does not run validators inside `chroot(2)`. `WithChroot`
  only guarantees that members and candidates live below the chroot and
  renders chroot-relative placeholders. That is what validators such as
  `nsd-checkconf`, which check paths against the configured chroot, need.
- **Validators are bounded** by `-cmd-timeout` like every backend command;
  processes a validator started are not killed (see
  [file-dir-link.md](file-dir-link.md) for the exact rules).

Members are limited to the plan's inline content size (512 KiB each) because
a set is published from its plan line, never from blobs.

## Plan wire

Schema v21 adds the `config_set` op (fields `name`, `members`, `validators`,
`chroot`, `staging_dir`, `deps`) and the report-only `config_set_member` op
(`name`, `member`, `path`, `deps` on the set). Destination apply re-validates
the whole description before any mutation. A member handle whose set did not
apply in the same run fails instead of silently never firing. See
[plan.md](plan.md).
