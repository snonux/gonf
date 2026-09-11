# Timer resource

Linux **systemd** timer management (Fedora and other systemd hosts). Enables and
starts `.timer` units, or stops and disables them. The `.timer` suffix is added
automatically when omitted.

```go
Timer("fstrim")                      // enable + start fstrim.timer
Timer("fstrim.timer", WithRestart)   // converge, then restart once
Timer("backup", WithUser)            // systemctl --user
NoTimer("oldjob")                    // stop + disable
```

Requires sufficient privileges for system timers (root / `sudo`), same as
`Service`. User timers (`WithUser`) use the calling user's systemd session.

Unit files themselves are not written by this resource — install them with
`File` / `Dir` (or packages) and depend on those resources if needed.

## Options

| Option | Meaning |
|--------|---------|
| `WithUser` | Use `systemctl --user` |
| `WithRestart` | Restart the timer once when already active |
| `IsAbsent` / `NoTimer` | Stop and disable |
| `DependsOn` | Apply after other resources |

## Live tests

Creates disposable `gonf-timer-live.{service,timer}` units, then Present/Absent.

```bash
# user timer (no sudo; must not run as root)
env GONF_RUN_TIMER_TESTS=1 go test ./resource/timer/ -run LiveUserTimer -v

# system timer (needs root)
sudo env GONF_RUN_TIMER_TESTS=1 go test ./resource/timer/ -run LiveSystemTimer -v
```
