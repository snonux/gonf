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
```

Jobs are stored in the target user's crontab between markers:

```cron
# BEGIN GONF Cron[backup]
PATH=/usr/bin:/bin
0 2 * * * /usr/local/bin/backup.sh
# END GONF Cron[backup]
```

`Run`, `push`, `apply`, and `fleet` all go through the one apply engine: Cron
records a `cron` plan op (name, user, command, schedule, env) that is applied
like any other resource (see [plan.md](plan.md)).

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
| `IsAbsent` / `NoCron` | Remove the named job |

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
