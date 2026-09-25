# Cron resource

Puppet-inspired crontab management for Linux, FreeBSD, NetBSD, and OpenBSD.

```go
Cron("backup",
    WithCommand("/usr/local/bin/backup.sh"),
    WithCronUser("root"),   // default root
    WithMinute("0"),
    WithHour("2"),
    WithCronEnv("PATH=/usr/bin:/bin"),
)
NoCron("backup", WithCronUser("root"))

// Compact spelling, the identical plan op:
CronAt("backup", "0 2 * * *", "/usr/local/bin/backup.sh",
    WithCronUser("root"), WithCronEnv("PATH=/usr/bin:/bin"))
```

`CronAt(name, schedule, command, opts...)` is `Cron(name,
WithSchedule(schedule), WithCommand(command), opts...)`; `WithSchedule("0 2 *
* *")` sets the five fields in one option (a later per-field option overrides
its field). A schedule that is not five portable fields is a declaration
error; `@reboot` and the other `@` directives stay unsupported.

Jobs are stored in the target user's crontab between markers:

```cron
# BEGIN GONF Cron[backup]
PATH=/usr/bin:/bin
0 2 * * * /usr/local/bin/backup.sh
# END GONF Cron[backup]
```

`Run`, `push`, `apply`, and `fleet` all go through the one apply engine: Cron
records a `cron` plan op (name, user, command, legacy command, schedule, env) that is applied
like any other resource (see [plan.md](plan.md)).

Gonf accepts the portable five-field Linux/BSD cron syntax: numbers, `*`,
lists, ranges, positive `/step` values, plus three-letter month and weekday
names. It rejects `@` directives and malformed schedules before changing a
crontab.

When `WithCronUser` matches the process user, gonf omits `crontab -u` (Linux
rejects `-u` for your own account without privileges). Other users still use
`crontab -u USER` and typically need root/`doas`/`sudo`.

## Options

| Option | Meaning |
|--------|---------|
| `WithCommand` | Command to run (required for present) |
| `WithCronUser` | Crontab owner (default `root`) |
| `WithMinute` / `WithHour` / `WithMonthday` / `WithMonth` / `WithWeekday` | Schedule fields (default `*`) |
| `WithCronEnv` | Environment line `KEY=VAL` above the job |
| `WithSchedule("m h dom mon dow")` | All five schedule fields at once |
| `WithLegacyCommand` | Remove unmanaged entries with this exact command, on any schedule, before creating this job; cannot be used with `NoCron` |
| `IsAbsent` / `NoCron` | Remove the named job |

## Adopting existing entries

A present job adopts the unmanaged entries that are already this job, so
migrating a hand-written line into gonf does not leave it running twice.
An entry is identical when, in the crontab of the job's own user only:

- it is a portable five-field entry outside every valid Gonf block;
- its five schedule fields equal the job's, compared as text after
  splitting on blanks (`0  6` matches `0 6`, `00 6` does not);
- its command equals `WithCommand` exactly, byte for byte;
- the job has no `WithCronEnv` (its lines would change the environment).

While the job has no block yet, the first identical entry is replaced in
place by the job's block, so the job keeps the environment (the
`NAME=value` lines above it, another Gonf block's `WithCronEnv` lines
included) it ran with. Every further identical entry, and every identical
entry once the block exists, is a duplicate run and is removed. Before
v0.21.1 the block was always appended at the end, so an entry followed by
any `NAME=value` line (for example another job's `WithCronEnv PATH=...`
block) was left in place and the job ran twice. A later change to the job
rewrites its block at the end of the table, like any changed block.

Anything else is left alone. `WithLegacyCommand(cmd)` is the explicit
opt-in for a different old command or the same command on another
schedule: it matches the command only, whatever the schedule and the
environment lines, removes those entries, and appends the block at the
end. Passing `WithLegacyCommand` with the job's own command still
works (and keeps `legacy_command` in the plan); an entry that is also
identical to the job is replaced in place rather than removed;
dropping it now relies on the default, which is stricter: the old line's
schedule must match too, and dropping it removes `legacy_command` from the
recorded op. A malformed Gonf marker disables both kinds of adoption for
that apply. `NoCron` adopts nothing.

Each reconciliation holds an advisory lock for that crontab across its
read/merge/write transaction. This prevents two Gonf processes from losing
each other's updates; other programs that write the same crontab must use the
same operational serialization.

## Failure output

A failing `crontab` read or write reports its exit code and arguments but only
the sizes of its stdout and stderr, for every job, sensitive or not: one table
holds every job's lines, so its output may quote another job's secret (see
[secrets.md](secrets.md)). To see the message itself, run `crontab -l` (or
`crontab -`) as the same user by hand.

## Live tests

```bash
# root crontab (needs privilege)
sudo env GONF_RUN_CRON_TESTS=1 go test ./resource/cron/ -run LiveCronRoundTrip -v

# per-user crontab + env (must run as non-root; do not use sudo)
env GONF_RUN_CRON_TESTS=1 go test ./resource/cron/ -run LiveCronPerUser -v

# cross-compile + remote (example FreeBSD)
GOOS=freebsd GOARCH=amd64 go test -c -o /tmp/cron_freebsd.test ./resource/cron/
scp /tmp/cron_freebsd.test paul@f0.lan:
ssh paul@f0.lan 'doas env GONF_RUN_CRON_TESTS=1 ./cron_freebsd.test -test.v -test.run LiveCronRoundTrip'
ssh paul@f0.lan 'env GONF_RUN_CRON_TESTS=1 ./cron_freebsd.test -test.v -test.run LiveCronPerUser'
```
