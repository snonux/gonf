# DSL verification, 2026-10-11 (l93)

Combined revisions under verification:

| Repository | Commit |
|---|---|
| gonf | `3fa91a5a3551a6fcc5cae01226086c7162b0f4b7` plus this fixture/docs change |
| conf | `dd4ef807d561a7fbbf5a5bb824ebf9c606ac70fe` |
| dotfiles | `94814b209c168574f3c32f6f5279727c261cc885` |

An external temporary `go.work` selected all three modules. Consumer dependency
pins and `go.mod`/`go.sum` were untouched. No tests were added to conf recipes.
Consumer source and generated descriptions were unchanged. The pre-existing
private conf cache was left alone.

## Controller checks

- `go build -p 1 ./...`, full `go test -p 1 -shuffle=on ./...`,
  `go vet -p 1 ./...`, `errcheck ./...` and `staticcheck ./...` passed in gonf.
  These compile every tutorial example too.
- Gonf's test run used `GOWORK=off`: two compile-negative tests create temporary
  modules, which cannot inherit an unrelated external workspace. The initial
  workspace run failed only those two harness checks; the standalone rerun
  passed the entire suite.
- Both consumers passed full build/test/vet/errcheck against the temporary
  workspace and exact gonf source above. Conf reported no test files;
  dotfiles' composition-root tests passed. Both passed
  `staticcheck -checks=inherit,-ST1001 ./...`; ST1001 rejects the dot imports
  their recipe conventions explicitly require. No style exceptions changed.
- Receivers built for Linux amd64/arm64, FreeBSD amd64, NetBSD arm64 and
  OpenBSD amd64. Live receivers used `/tmp/gonf-l93-receiver`, preserving
  installed system binaries. The initial pi2 strict preview refused the absent
  receiver; after staging, strict preview succeeded.
- `python3 examples/dsl-e2e/verify.py <fixture> <receiver>` passed, including
  runs with hostile inherited remote-delivery variables. Its assertions check
  actual packaged/file bytes, guard decisions, dry-run, convergence, drift,
  preservation, optional absence, required missing source and wrong types.

## Offline consumer recording

The capture entry points registered each consumer's unchanged task composition,
then used `RecordPlanTo` with `MemoryStore` and an in-process synthetic secret
provider. They never imported the vault provider or applied recorded plans.
The source roots pointed at the exact checkouts above.

| Consumer | Profiles | Plans per profile | Operations per profile |
|---|---|---:|---:|
| conf | fedora, rocky, freebsd, openbsd, netbsd, darwin | 148 | 2570 |
| dotfiles | fedora, rocky, freebsd, openbsd, netbsd, darwin | 37 | 335 |

All available tasks and aggregates were recorded, except the four unrelated
Operational conf tasks: `wireguard_mesh_install`, `frontends_acme_invoke`,
`frontends_irc_bouncer`, and `frontends_ping`. Plans were exactly equal across
profiles: their destination-side OS/profile/hostname guards remain serialized.
This checks controller recording portability; it does not claim execution on
an unavailable macOS host. No actual secret values were printed.

## Disposable live fixture

Power preflight was `f3sctl power status`: f0-f3 and r0-r2 were already up,
fans and AC on. No power command was needed. Pools were ONLINE, f0 was CARP
MASTER, f1 BACKUP, f0's Rocky VM running, all three k3s nodes Ready, and r0's
NFS mount readable before work began.

The fixture used actual `PreviewToContext`/`PushToContext` transport, record,
packaging and destination application, not fake runners. Full tree fixtures
used dry-run push after a strict preview of their non-blob subset, because
strict preview intentionally refuses staging blob-backed trees.

| Host | Portable fixture dry / first / repeat | Included drift / repeat |
|---|---|---|
| f0 (FreeBSD) | 5 would-change / 5 changed / 5 ok, 0 changed | 1 changed / 0 changed |
| r0 (Rocky Linux) | 5 would-change / 5 changed / 5 ok, 0 changed | 1 changed / 0 changed |
| pi0 (NetBSD arm64) | 5 would-change / 5 changed / 5 ok, 0 changed | 1 changed / 0 changed |

Remote byte assertions verified repaired public postcards, preserved excluded
local postcards, absent excluded controller payloads and absent guarded files.
`fixture_copy` included its `RegisterGroup`-registered prerequisite;
`WithShellVars` produced the expected ordered quoted settings. Required and
optional controller checks ran before packaging; sources were never read on
the remote destination.

On f0 and r0, explicit privileged tasks created a disposable `gonfy_e2e`
account and enabled an inactive `gonfy_e2e` service. Both repeated cleanly.
The service definition's start/stop marker stayed absent throughout: enablement
never started or stopped the service. The account shell was then deliberately
changed to csh/bash, boot enablement disabled and a harmless live sysctl drifted:

- f0 `kern.msgbuf_show_timestamp`: original 1, drift 0, repaired 1.
- r0 `fs.inotify.max_user_watches`: original 86388, drift 86389, repaired 86388.

Dry-run predicted the shell, enablement and live sysctl changes. Actual push
repaired all three. The persistent sysctl file stayed unchanged while its live
command repaired drift. Repeat pushes had zero changes. A finally block restored
the original kernel value regardless of the verification result.

## Migrated consumers on real hosts

Temporary CLI wrappers retained each consumer's original registration and secret
provider, routing delivery to the separate receiver path. Real providers were
used where consumer recipes require them; only status/summary evidence was
reported. Each target was previewed before live apply and applied twice.

| Target / selected tasks | First apply | Repeat apply |
|---|---|---|
| f0: baseline rc.conf/sysctl, bhyve rc.conf, NFS rc.conf/sysctl, relayd rc.conf, zfskeys rc.conf, node_exporter | 19 ok, 6 skipped, 0 changed | same |
| f1: NFS rc.conf | 5 ok, 0 changed | same |
| r0: NFS client sysctl | 1 ok, 1 skipped, 0 changed | same |
| pi2: Pi-hole Compose | 2 ok, 1 skipped, 0 changed | same |
| fishfinger: Gogios/Foostats | 28 ok, 2 changed, 1 skipped | 29 ok, 1 changed, 1 skipped |
| earth: agents/scripts/calendar/gitconfig/systemd_user | 53 ok, 24 changed, 16 skipped | 77 ok, 0 changed, 16 skipped |
| earth: WireGuard boot enablement/fish shell | 5 ok, 2 changed | 7 ok, 0 changed |
| rocky: agents/scripts/calendar/gitconfig | 37 ok, 25 changed, 13 skipped | 62 ok, 0 changed, 13 skipped |
| rocky: fish shell (root SSH) | 2 ok, 0 changed | same |

Fishfinger's repeated changed action was solely the pre-existing
`IsLatest` package policy (`pkg_add -u gogios` runs on each apply); configuration
files and services converged. This is intentional package action reporting,
not a claim that the repeat summary was zero. Rocky home tasks ran as paul;
its system task ran as root, matching its restricted sudo policy.

Critical conf was processed one host at a time. f0 stayed MASTER with NFS and
stunnel running; f1 stayed BACKUP with both stopped. Pools remained ONLINE and
all k3s nodes Ready. Earth WireGuard runtime states stayed wg0 inactive and wg1
active before and after its boot-only setting changes. No roaming tunnel
start/stop/up/down command was invoked.

MacOS had offline recording coverage only; no macOS target was available. At
this point the other fleet members were not needed for representative coverage;
the later full-fleet pass below covers the reachable ones. Production consumer
convergence changes intentionally remain in place; the separate disposable
fixture state is removed after verification.

## Full fleet convergence, 2026-10-12 (8b3)

This later release check extends the selected live coverage above. After
`f3sctl power on` and `f3sctl power f3 on`, f0--f3, r0--r2, fans and AC were
online; all k3s nodes were `Ready`, f0 was CARP MASTER and its pool was
healthy. A controller built from conf `dc2d765` with Gonf v0.25.0 applied each
non-operational aggregate to every registered live target, once to repair
pre-existing drift and once more for convergence. Gonf v0.25.1 contains only
the subsequent BSD CI and release-documentation change; both consumer modules
were then rebuilt from their published v0.25.1 dependency and reported that
version.

| Targets | Aggregate/tasks applied twice | Repeat result |
|---|---|---|
| f0--f3 | `freebsd` | 94/92/71/71 ok, 0 changed |
| f0--f2 | `garage` | 1 ok, 0 changed on every host |
| blowfish, fishfinger | `frontends` | configuration resources stable; only package refresh and zone-publication commands report actions |
| pi0, pi1 | `pis_netbsd`, `netbsd_goprecords_uptimed`, `netbsd_goprecords_upload` | 29 ok, 0 changed on every host |
| pi2, pi3 | `rocky_kernel_audit`, `pihole` | 13 ok, 0 changed on every host |
| pi2, pi3, r0--r2 | `rocky` | 35/35/12/12/12 ok, 0 changed |
| r0--r2 | `rnodes` | configuration resources stable; only unconditional `journalctl --flush` reports one command action per host |
| earth | `home`, `home_goprecords_upload`, `pkg_fedora`, all `system_*` tasks | 136/2/10/38 ok, 0 changed across normal and elevated chunks |
| rocky | `home`, `system_fish_shell` | 123 ok, 0 changed; 2 ok, 0 changed as root |

The first pass repaired one stale f3 VM memory setting, four frontend
configuration resources, two Earth user files and Rocky home configuration.
The repeat produced no `updated`, `created`, `removed` or changed managed-file
resource line. The frontend package policy (`pkg_add -u`) and NSD publication,
and r-node journal flush, deliberately run on every apply and are command
actions rather than configuration drift. Operational tasks were deliberately
excluded: `wireguard_mesh_install`, `frontends_acme_invoke`,
`frontends_irc_bouncer` and `frontends_ping`. Zen remained unreachable (`No
route to host`), so its two hostname-guarded Dotfiles tasks retain the prior
offline coverage and are not represented as live-tested.

The conf controller commands were:

```sh
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 freebsd-hosts freebsd
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 garage garage
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 frontends frontends
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 netbsd-pis pis_netbsd netbsd_goprecords_uptimed netbsd_goprecords_upload
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 rocky-pis rocky_kernel_audit pihole
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 rocky-all rocky
GONF_CONF_ROOT=/home/paul/git/conf /tmp/gonf-convergence-20261012/conf cluster -j 1 rocky-k3s rnodes
```

Each command was run a second time in the same order; the table records that
second invocation. The Dotfiles controller used the corresponding direct local
and `push` task invocations with `GONF_DOTFILES_ROOT=/home/paul/git/dotfiles`.

## Reproduction commands and final checks

The full race/shuffle run `GOWORK=off go test -p 1 -race -shuffle=on -count=1
./...` completed with terminal exit 0. This task compiled all tutorial recipes;
it did not recapture their chapter outputs (the preceding feature/guide tasks
had already done that).

For an equivalent temporary workspace and binaries, from the gonf checkout:

```sh
mkdir -p /tmp/gonf-l93
cat > /tmp/gonf-l93/go.work <<'WORK'
go 1.26.4
use (
 /home/paul/git/gonf
 /home/paul/git/conf/gonf
 /home/paul/git/dotfiles/gonf
)
WORK
GOWORK=off go build -p 1 -o /tmp/gonf-l93/fixture ./examples/dsl-e2e
GOWORK=off go build -p 1 -o /tmp/gonf-l93/gonf ./cmd/gonf
GOWORK=off GOOS=freebsd GOARCH=amd64 go build -p 1 -o /tmp/gonf-l93/gonf-freebsd ./cmd/gonf
GOWORK=off GOOS=netbsd GOARCH=arm64 go build -p 1 -o /tmp/gonf-l93/gonf-netbsd ./cmd/gonf
GOWORK=off GOOS=openbsd GOARCH=amd64 go build -p 1 -o /tmp/gonf-l93/gonf-openbsd ./cmd/gonf
GOWORK=off GOOS=linux GOARCH=arm64 go build -p 1 -o /tmp/gonf-l93/gonf-linux-arm64 ./cmd/gonf
python3 examples/dsl-e2e/verify.py /tmp/gonf-l93/fixture /tmp/gonf-l93/gonf
```

The fixture README lists source preparation and all transport variables. For
f0, after checking names are unused, preparing synthetic sources and staging
the separate receiver with `scp -P 22`, the exact delivery sequence was:

```sh
mkdir -p /tmp/gonf-l93/source/postcards/private
printf 'Gonfy welcomes you\n' > /tmp/gonf-l93/source/welcome.txt
printf 'public postcard\n' > /tmp/gonf-l93/source/postcards/public.txt
printf 'excluded canary\n' > /tmp/gonf-l93/source/postcards/private/secret.txt
printf 'scratch\n' > /tmp/gonf-l93/source/postcards/draft.tmp
export GONF_E2E_SOURCE=/tmp/gonf-l93/source GONF_E2E_GOOS=freebsd
export GONF_E2E_HOST=f0.lan.buetow.org GONF_E2E_USER=paul
export GONF_E2E_PRIVILEGE=doas GONF_E2E_RECEIVER=/tmp/gonf-l93-receiver
GONF_E2E_DELIVERY=preview /tmp/gonf-l93/fixture fixture_base excluded
GONF_E2E_DRY_RUN=1 GONF_E2E_DELIVERY=push /tmp/gonf-l93/fixture fixture_copy excluded
GONF_E2E_DELIVERY=push /tmp/gonf-l93/fixture fixture_copy excluded
GONF_E2E_DELIVERY=push /tmp/gonf-l93/fixture fixture_copy excluded
```

`consumer-wrapper.py` reproduces the temporary delivery-only CLI wrappers:
it copies main into the output, retains task registration and secret-provider
initialization, and replaces the CLI call with explicit preview/push routing.
It refuses to overwrite an existing output. The following creates fresh
wrappers; outputs are temporary, never edits to consumer source:

```bash
python3 examples/dsl-e2e/consumer-wrapper.py /home/paul/git/conf/gonf/cmd/gonf/main.go /tmp/gonf-l93/conf-live.go
python3 examples/dsl-e2e/consumer-wrapper.py /home/paul/git/dotfiles/gonf/cmd/gonf/main.go /tmp/gonf-l93/dotfiles-live.go
GOWORK=/tmp/gonf-l93/go.work go build -p 1 -o /tmp/gonf-l93/conf-live /tmp/gonf-l93/conf-live.go
GOWORK=/tmp/gonf-l93/go.work go build -p 1 -o /tmp/gonf-l93/dotfiles-live /tmp/gonf-l93/dotfiles-live.go
export GONF_CONF_ROOT=/home/paul/git/conf
export L93_HOST=f0.lan.buetow.org L93_USER=paul L93_PRIVILEGE=doas L93_PORT=22
conf_tasks=(
 freebsd_baseline_rc_conf freebsd_baseline_sysctl freebsd_bhyve_rc_conf
 freebsd_nfs_rc_conf freebsd_nfs_sysctl freebsd_relayd_rc_conf
 freebsd_zfskeys_rc_conf freebsd_monitoring_node_exporter
)
L93_DELIVERY=preview /tmp/gonf-l93/conf-live "${conf_tasks[@]}"
L93_DRY_RUN=1 L93_DELIVERY=push /tmp/gonf-l93/conf-live "${conf_tasks[@]}"
L93_DELIVERY=push /tmp/gonf-l93/conf-live "${conf_tasks[@]}"
L93_DELIVERY=push /tmp/gonf-l93/conf-live "${conf_tasks[@]}"
```

The other consumer task IDs passed to the same sequence were:

| Target | Task names |
|---|---|
| f1 | `freebsd_nfs_rc_conf` |
| r0 | `rnodes_nfs_client_sysctl` |
| pi2 | `pihole_compose` |
| fishfinger (port 2, rex/doas) | `frontends_gogios frontends_foostats` |
| earth (local CLI, `-privilege=sudo`) | `home_agents home_scripts home_calendar home_gitconfig home_systemd_user system_wireguard system_fish_shell` |
| rocky (paul, no privilege) | `home_agents home_scripts home_calendar home_gitconfig` |
| rocky (root/sudo) | `system_fish_shell` |

For dotfiles, set `GONF_DOTFILES_ROOT=/home/paul/git/dotfiles` and invoke
`dotfiles-live` for remote sequences. Earth used the original built CLI with
`-dry-run` before two actual local invocations. Complete source trees used
regular dry-run push when strict preview would require staging blobs.

All disposable fixture trees, accounts, same-name groups, service definitions,
boot-enable entries and receiver paths were removed; their absence and the
original sysctl values were explicitly checked on every affected host. f0's
first cleanup found its group already removed by `pw userdel`; a guarded retry
and independent account/group absence assertions succeeded. pi0 lacks `unlink`;
cleanup used `find /tmp/gonf-l93-receiver -delete` and verified both receiver and
fixture tree absent. No failed cleanup was counted as success. Final power
status remained all hosts up, f0 MASTER/NFS running, f1 BACKUP/NFS stopped,
pools ONLINE, NFS readable and all k3s nodes Ready.

The final wrapper generator was exercised on both actual consumer mains and
both generated Go programs compiled through the same workspace. Negative
checks verified malformed entry-point input creates no output and an existing
output remains byte-identical after refusal. Installed f0/r0/pi0 gonf binaries
still reported their original version 0.24.0 and schema 27 after receiver
cleanup. Verification logs and synthetic captures remain under the private
controller scratch directory `/tmp/gonf-l93`; durable results are above.
