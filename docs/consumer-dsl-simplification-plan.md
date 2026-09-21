# Gonf consumer review and DSL simplification plan

Date: 2026-09-20. Status: investigation and proposal. The P0 baseline is
recorded below; it authorizes neither implementation nor deployment. Later
work packages remain separately scoped.

## Recommendation

Keep the existing Go DSL and its small primitives. Simplify the repeated
configuration-management mechanisms in gonf, move native configuration text out
of Go, and leave service topology and operational policy in the consumers.
Do not build a second configuration language or a universal service framework.

The conf consumer is substantially harder to maintain than dotfiles. Its frontend
recipes mix host selection, config rendering, candidate validation, live-file
installation, account repair, cron adoption, and restart wiring. Dotfiles' ordinary
`SyncDir`, `InstallFile`, and `Link` declarations are already reasonably concise.
Reducing every one-line task to a table would hide useful task boundaries.

Correctness must precede cosmetic simplification. Compilation passes, but the
review found malformed NSD output, DNS and cron convergence problems, a monitoring
dependency regression, and a missing privilege declaration in dotfiles.

Foostore is a plausible controller-side secret provider, preferably through its
KeePass backend. It is **not yet suitable as an unattended provider through its
current `get`/`cat` commands**. Add a machine-facing read contract before wiring it
into gonf, and address secrets in plans as well as secrets at rest.

This supplements [the Rex gap audit](conf-rex-gaps.md). That document's capability
matrix is not proof of recipe correctness or completed Rex retirement; some of
its consumer-status and aggregate descriptions are stale.

## Scope and evidence

Reviewed all current Go consumer source files: 15 in `conf/gonf`, and five in
`dotfiles/gonf`, including the Magefile: 3,016 lines in total. Also inspected both
wrappers, module pins, task composition, primary generated/installed configuration
templates, referenced asset paths, remaining Rexfiles and their task mappings,
deployment instructions, and relevant gonf resource/recording/transport code.
Foostore inspection covered its CLI, backend selection, lookup/output behavior,
credential loading, KeePass representation, and legacy encryption implementation.

Snapshot:

| Repository | HEAD | Relevant version/state |
| --- | --- | --- |
| gonf | `2c72427` | Local v0.14.1 implementation, plan schema 16 |
| conf | `f029fb2` | Consumer pins gonf v0.14.0 |
| dotfiles | `d6a90c5` | Consumer pins gonf v0.12.2 |
| foostore | `f04985c` | v0.9.0; implementation defaults to KeePass |

Pre-existing edits in conf's `gonf/paths/paths.go` and
`f3s/docs/taskwarrior-s3-sync.md`, and foostore's untracked/modified local tooling
files, were left untouched. Observations use the working-tree sources.

Verification performed:

- `go test ./...` and `go vet ./...` passed in gonf and both pinned consumer
  modules. Both consumers report **no test files**; their results establish
  compilation/static-check health, not service behavior.
- An isolated temporary Go recorder imported both consumers against local gonf,
  supplied synthetic NSD/Garage secrets, and recorded into memory without applying
  anything. All 52 controller-activated conf task/aggregate plans and all 26
  controller-activated dotfiles task/aggregate plans recorded successfully.
  This is not an all-profile or all-OS execution test.
- That recorder confirmed valid JSON syntax for Gogios and 20 repeated resource
  IDs in the `home` aggregate. The NSD directive-prefix defect it recorded was
  fixed by `c52`.
- Recording pinned dotfiles' explicit Fedora package task produced 39 package
  operations, none marked for elevation. Recording `home_prompts` succeeded;
  nested `Run` does not apply resources during recording.

No live hosts were contacted, no system configuration was applied, no actual
secret values were inspected or printed, and no native OpenBSD validators were
run. This is a consumer/DSL review, not a complete security audit of foostore or an
audit of every unrelated shell script, Helm chart, and application in either repo.

## Findings and resolution status before calling the migration complete

### F1 — NSD directive prefixes (fixed by `c52`)

The P0 recorder found literal backslash-t directive prefixes in the rendered NSD
configuration. `c52` corrected that rendering, so current candidates use
ordinary directive whitespace. Keep the NSD validation barrier and validate
staged candidates with `nsd-checkconf` on OpenBSD as required by the separate
validation work.

### F2 — DNS cannot converge, and two writers own the live zones (high; source-confirmed)

`MailDNS.NSD` passes `time.Now().Unix()` to every zone renderer (line 88).
Recording again in a different second changes every SOA serial even when the
configuration is identical. The zone files therefore change and arm an NSD restart.

Additionally, `frontends/scripts/dns-failover.ksh` rewrites the same live zones
using the current failover roles, while gonf renders addresses from the static
`Master` and `Standby` constants. An ordinary configuration apply can undo an
active failover until the failover job rewrites the files again. This writer
conflict predates some of the Go port; matching Rex does not make it safe.

The following is the approved ownership contract, recorded by task `d52`. It
is a design correction: until `e52` is implemented, the current direct-file
writers described above will remain in place and will not satisfy this
contract. The statements below describe the future publisher, not current
behaviour.

#### DNS publication and SOA contract

`frontends` owns the declarative inputs: the zone templates under
`frontends/var/nsd/zones/master`, the zone list and frontend topology in
`gonf/frontends/data.go`, and the source-controlled F3S host list. Those inputs
will describe records and role placeholders. They will not own a live zone
file, an SOA serial, a health result, or the active role. In particular,
`Master` and `Standby` are service-routing defaults; neither constant will
select a DNS writer.

`DNSPublisher = "blowfish"` is the direct, stable publisher identity; it must
not be derived from the service-routing `Standby` value. The contract designates
**blowfish** as the one effective-zone publisher.
It will be the NSD master and the only host permitted to change
`/var/nsd/zones/master/*.zone`, committed role state, or SOA serials. Fishfinger
will be an NSD transfer slave and will never edit an effective zone; it will
receive committed versions by zone transfer. Blowfish will remain the publisher
when a health failover directs public records to fishfinger: service role and
publication ownership are different concerns. If blowfish is unavailable, its
already published zones will continue to be served by fishfinger. A second
simultaneous frontend failure is outside this two-node failover contract; the
slave will not publish independently.

`e52` will implement a frontend-specific publisher wrapper on blowfish. This
wrapper, rather than a `DNSZoneSet` resource or callback, will own the
target-local lock, durable role and per-zone serial state, rendering, validation,
and the journalled state-and-zone transaction. A future `DNSZoneSet` helper may
parse, canonicalise, stage, or validate immutable candidates, but will not own
the lock, role state, rendering, or publication transaction. This is the chosen
boundary; it is not a generic callback contract.

The wrapper will own durable failover state, initially the declared service
role. A failed health decision, timeout, malformed state, or unavailable
candidate will keep the committed role and zones unchanged. A successful
health-triggered promotion will record the new role only as part of a successful
publication. The weekly `date +%U` rotation will not be a role source:
wall-clock jumps, DST, reboot, and clock correction must not alter DNS.
Returning from an active failover will require a separate verified promotion
(or an explicit, authorised operator action), rather than an inference from the
current week. Until then, the legacy script's temporary `current_*` and address
files are current implementation caches; the wrapper will not treat them as
shared authority.

One target-local publisher lock will cover the complete
read-render-validate-publish transaction. Gonf apply, cron, and a manual
recovery command will call that wrapper, never write zones independently;
cron's `-s` option will be only secondary protection. While holding the lock,
the wrapper will reread committed role state and zones, render the complete
effective candidate set, and validate every candidate zone plus its NSD
configuration. It will recheck any input whose version changed while it waited
for the lock. No lock is claimed to be a distributed mutex: the fixed publisher
host and NSD transfer topology remove the need for one.

All effective zones will use `blowfish.buetow.org.` as the SOA MNAME. It names
the stable publication primary, not the currently active public-service role,
and will therefore remain unchanged during a service failover. Rendering will
set this MNAME explicitly; a template's present MNAME will not override that
policy.

The serial will belong to each committed effective zone. The fixed initial
serial for a newly created zone will be unsigned 32-bit decimal `1`
(`uint32(1)`). A migration from an existing zone will parse that zone's serial
as an unsigned base-10 integer in `[0, 4294967295]` and use its RFC 1982
successor, `(old + 1) mod 2^32`; it will never replace an existing serial with
the initial value. A missing, duplicate, non-apex, or unparseable committed SOA
serial will be a hard publication failure. The wrapper will leave every zone and
state file unchanged and require operator repair; it will not guess, reset, or
advance the serial. This single-writer, one-step rule will preserve serial order
for normal secondaries, including wraparound, provided a secondary is not left
more than half the sequence space behind. A timestamp or content hash will never
be a serial source, so clock changes and repeated rendering cannot move it.

For the no-change decision, the wrapper will parse both the candidate and the
committed master file as a single zone and require exactly one apex SOA in each.
It will expand `$ORIGIN`, relative owner names, and inherited TTLs; discard
comments and directives; encode the parsed RRs as DNS wire data; sort the
encodings lexicographically; and compare the resulting RR multisets after
replacing only the apex SOA serial with an omitted value. It intentionally does
not attempt a partial case-fold of domain-name RDATA: DNS has many record forms
and an incomplete canonicalizer could misclassify opaque text as a name and
hide a real change. Controller templates must keep owner and RDATA spelling
stable; a spelling-only case change conservatively causes publication. Comments,
whitespace, directives, record order, and the serial itself will not cause
publication; every other parsed RR value, including the SOA MNAME, RNAME,
refresh, retry, expire, and minimum fields, will. Parse failure, an origin
mismatch, or an invalid candidate will be a hard failure, not a comparison that
can be treated as changed or unchanged.

On canonical equality, the wrapper will publish nothing: no serial advance,
zone write, state write, or NSD reload. On a difference, it will assign one
successor serial to every changed zone before staging validation.

Validation, replacement, reload, and state persistence will form one success
condition. The wrapper will stage every zone, key, and configuration file before
any live replacement; build the complete preimage in a temporary journal; then
atomically publish that snapshot and its incomplete intent before the first
replacement. It writes the new state inside that rollback boundary and removes
the intent only after NSD reload succeeds. If validation, replacement, state
persistence, or reload fails, it will restore the saved zones, key,
configuration, and state, attempt an NSD reload of that restored set, and
return a hard failure. The wrapper will recover an intent-marked journal before
accepting later publication. Multiple zone-file replacements are not
filesystem-atomic; the journal and recovery rule define the required atomic
state-and-zone outcome while the publisher lock is held. A failed reload with
an unsuccessful rollback will be a visible degraded state, never a successful
publication.

The wrapper will need record/codec/apply coverage for repeat apply, changed
input, active failover, clock changes, serial wrap, lock contention, invalid or
unparseable zones, failed replacement/reload, crash recovery, and retry after
failure before either frontend task uses it.

### F3 — Cron migration is incomplete and one cleanup is self-defeating (high)

The NSD legacy-cleanup predicates in `maildns.go` (lines 25–41) protect markers
named `Cron[root/frontend-nsd-failover]`. Core
[cron.go](../resource/cron/cron.go) emits `Cron[frontend-nsd-failover]` markers;
the user-qualified resource ID is not the crontab marker name. Consequently, an
already managed command is detected as legacy, removed, and recreated on the
next apply.

The rsync and PF recipes add Gonf blocks but have no equivalent adoption of their
old unmanaged Rex cron lines. Core Cron preserves unrelated lines. A host still
carrying the Rex entries therefore gets duplicate jobs after migration.

Both shell cleanup implementations also turn a failed `crontab -l` into empty
input and match command substrings. Do not broaden this approach. Add opt-in
legacy adoption to core Cron using its own parser/marker identity, a deliberately
specified command match, and fail-closed read errors. Preserve unrelated jobs,
environment lines, and other Gonf blocks. Test no-crontab separately from command
failure, old/new/mixed states, malformed markers, repeat apply, and concurrent
access. External crontab writers still need an operational ownership rule.

### F4 — PF validation happens after replacing the live file (high; source-confirmed)

`Web.PF` in [web.go](../../conf/gonf/frontends/web.go) (line 113) installs
`/etc/pf.conf`, then runs `pfctl -n -f /etc/pf.conf`. A rejected candidate has
already replaced the persistent live configuration, even though loading it into
the kernel is blocked. Validate a separate candidate before the live write.

The other validation recipes demonstrate the desired ordering, but their fixed
staging paths need private, collision-resistant ownership and lifecycle rules.
`validatedConfig` also claims candidates are root-readable only while HTTPD's
candidate is actually created with mode 0644.

### F5 — Gogios checks depend on a physical host instead of the master role (high)

`addHostChecks` in [monitoring.go](../../conf/gonf/frontends/monitoring.go)
(line 256) uses `MustServer(Master).FQDN` for non-standby HTTP/TLS dependencies.
The Rex template uses `master.buetow.org`; other Go-generated checks still use
that role name. During failover, ordinary service checks can be suppressed by a
failed fishfinger ping even when the current master is healthy.

Restore role-based dependencies for service names while retaining physical-host
dependencies for checks specifically about physical hosts. JSON parsing alone
does not catch this regression; compare the actual dependency graph.

### F6 — Fedora package task lacks privilege; platform guards need repair (medium)

`dotfiles/gonf/tasks/pkg.go` declares `type Pkg struct{}` rather than carrying
`RequiresRoot` or an equivalent task option. The README's ordinary-user
`./gonf pkg_fedora` invocation has no elevation contract; core invokes `dnf`
directly. Declare privilege on the package task without elevating home tasks.

`HomeTasks.SystemdUser` has no Linux/systemd guard, yet `home` includes it and
other tasks explicitly support FreeBSD. The aggregate will attempt unsupported
systemd resources there. Existing custom `WhenX(Facts)` methods are also opaque
controller predicates: gonf explicitly refuses opaque-only tasks on push rather
than silently applying them remotely. Prefer serializable task options for
GOOS/profile/hostname rules; test both local selection and remote recording.

### F7 — ACME's change detection and certificate selection are misleading (medium)

In [acme.sh.tmpl](../../conf/gonf/frontends/assets/acme.sh.tmpl), `handle_cert`
prints a skip message and uses bare `return` (lines 27–40). That returns the
successful `echo` status, and callers set `has_update=yes`. Even skipped work can
therefore trigger relayd reload/restart and SMTPD restart. Successful invocation
also does not prove certificate bytes changed.

`NonStandbyHosts` additionally causes the script to request ipv4/ipv6 names for
which `acme-client.conf.tmpl` deliberately creates no separate domain entry:
they are SANs of a parent certificate. Unify the certificate-request list and
configuration data, distinguish skip/failure/change, and reload only consumers
whose certificate material changed. Keep certificate issuance an explicit
operational action, excluded from normal setup aggregates. These are inherited
workflow issues as well as migration-review findings.

### F8 — Aliases, optional inputs, and wrappers hide intent (medium/low)

- `HomeTasks.Prompts` calls `_ = Run("home_agents")`; both alias and target match
  `^home_`, explaining the repeated agent resource IDs in `home`. Nested recording
  is supported, so this is redundant expansion and ignored-error behavior, not
  evidence of an unexpected mid-plan apply. Add first-class task aliases that do
  not participate in aggregate expansion twice.
- `Agents` and `Calendar` silently return on **any** `os.Stat` error. Missing
  optional sources may legitimately skip, but permission/I/O errors should fail
  visibly. Make controller-source optionality distinct from target-side guards.
- Both `gonf.sh` wrappers pass unquoted `$@`; whitespace and glob arguments are
  not preserved. Their working-directory requirement is documented for conf,
  but a location-independent wrapper would be easier to use correctly.
- Recipes contain `/home/paul/git/shuriken.sh`, `/home/paul/git/foostats`, and
  home-relative checkout assumptions. Configure controller asset roots once;
  fail early with actionable missing-input messages.
- `PkgRepo` appends an unquoted PKG_PATH export, while Rex installed a quoted
  equivalent. It does not adopt/replace that old line, leaving duplicate settings.

### F9 — “Config deployment” is not the same as first-host provisioning

Garage's task installs TOML and manages the service; it does not install Garage,
create its group/data storage, or provision the cluster. Its API and docs should
make that prerequisite explicit rather than implying a full deployment.
Similarly, some unattended `Cron`/`Units` tasks rely on separately installed
scripts/packages, and frontend services assume existing certificates or packages.
Define setup aggregates and diagnostic subtask contracts rather than relying on
alphabetical aggregate order for prerequisites.

The old frontend `base` task references `scripts/tmux-edit-send`, but that asset
is not tracked/present in the reviewed checkout and the Go recipe omits it.
Classify this explicitly as retired or restore it with an identified source;
do not silently claim parity or reintroduce a dangling dependency.

### F10 — Secrets protection stops short of a secret-aware plan model

`MustSecret`/`OptionalSecret` securely constrain file lookup and preserve bytes,
but return ordinary strings. NSD places the value into file content; Garage puts
it into `template_data`. Both are plaintext-recoverable plan material (base64 is
not encryption). The CLI supports `plan -stdout`, with no sensitivity distinction
in `plan.Op`. Validator stderr can also disclose content unless handled carefully.

Current protections should be retained: private plan files/directories, protected
file traversal, memory-backed push packaging, and SSH transport. Do not claim
that foostore would keep plaintext out of plans or out of destination files.

Also note: remote `push -n` is **not wholly non-mutating**. Remote binary bootstrap
is performed before dry-run apply and can install/update gonf. This is why no
remote dry-runs were used in this investigation. A future preview command needs
an explicit no-bootstrap/no-write contract, or clear separation from preparation.

## What should become core, and what should stay consumer-side?

The following are proposed APIs/capabilities, not features already available.

| Pattern | Recommended home and shape | Priority |
| --- | --- | --- |
| Stage → validate → install one config | Core file validation capability, preferably `WithValidation` on existing file APIs | First |
| Validate config plus tables/key/includes before publication | Core `ConfigSet` resource; named members, dependency-safe validation barrier, member change handles | First |
| Adopt an old unmanaged scheduled job | Opt-in core Cron adoption, not consumer AWK | First |
| Install existing systemd units/drop-ins, reload once, activate selected units | Core `SystemdUnits` composition; reuse `SystemdTimer` for generated simple timers | Next |
| Create local users/groups; opt into updating specific existing attributes | Extend core User deliberately; add standalone Group only with an explicit group-only use case | Next |
| `/etc/login.conf.d` installation plus database rebuild | Small BSD login-class resource with real platform behavior checked | Next |
| Config lines in shared rc/profile/daily files | Core keyed/block editing where exact-line edits are insufficient; explicit ownership, not whole-file replacement | Next |
| Per-host typed data inside destination guards | API helper, e.g. `ForHosts[T](key, fn)` using current cluster inventory | Next |
| Secrets from an external store | Provider interface plus sensitive references/content handling; foostore adapter outside recipe bodies | Parallel prerequisite track |
| Zone publication and serial lifecycle | Frontend publisher wrapper; optional non-owning DNS-zone helper | `d52` contract; `e52` implementation |
| OS update windows, reboots, drain policy, service restart allowlists | Consumer recipes and existing audited scripts | Keep local |
| Websites, routes, check catalogues, Garage sizing, failover roles | Consumer data and templates | Keep local |
| Repeated simple dotfile copies/links | Existing `SyncDir`, `InstallFile`, `SymlinkMap`; small local helpers where useful | Do not over-abstract |
| Identical root/group/mode options | Explicit immutable scoped file defaults if repetition remains after extraction | Defer until measured |
| Custom package repository policy | Consumer-defined package helper/defaults over existing `WithEnv`; no fleet URL in core | Keep local initially |

Avoid one giant `ManagedService` with dozens of switches. The existing
`Service(..., WithRestart, OnChange(config))` is short, readable, and makes the
restart policy visible. A helper should remove bookkeeping, not conceal
privilege, deletion, pruning, restart, reboot, or network effects.

### Validation resource contract

Render/resolve each candidate once per apply, create it in a private staging
location, validate using argv rather than interpolated shell text, then publish.
Only published live changes should arm downstream restart gates. A validator
must also run when repairing independently modified live content, not merely
when a retained candidate changes. Validation failure must leave all live inputs
untouched and must not restart/reload the service.

For multi-file configs, account explicitly for relative includes and chroots.
Validate the complete candidate set, and serialize overlapping publications.
Do not call several individual file renames an atomic transaction: document
partial-publication failure and recovery, or implement rollback with explicit
limits. Candidate paths need a typed placeholder supplied by gonf, not ad hoc
`strings.ReplaceAll` on paths inside config text.

Illustrative target for a simple case (new `WithValidation`/`CandidatePath`):

```go
cfg := InstallFile("/etc/httpd.conf", assets("httpd.conf.tmpl"),
    WithTemplateData(data),
    WithValidation("httpd", List("-n", "-f", CandidatePath)))
Service("httpd", WithRestart, OnChange(cfg))
```

Owners/modes remain explicit or come from a clearly declared scope; they are
omitted from this sketch only to focus on validation. SMTPD needs `ConfigSet`,
with an aliases member available to `OnChange` for `newaliases`; restarting for
every group member would lose that intentional distinction.

### Host/task readability

Replace repeated `ClusterHosts` → `MustHostValue[T]` → `WhenHostname` loops with
one typed iterator that preserves exactly those semantics. It must carry a
destination-side guard, not evaluate the target against the controller hostname.
Inventory remains the only host-value source, and missing/wrongly typed values
must fail before SSH. Do not accidentally read every host's private inputs when
deploying to one selected host; define selection and secret resolution together.

Use existing serializable task options for platform conditions. Preserve public
task names and descriptions. Keep straightforward method registration for tasks
with real bodies; add explicit alias/aggregate membership support rather than
encoding safety policy in a growing regex. Setup aggregates should list groups
of setup tasks; certificate issuance, disabled services, and diagnostics must
stay visibly separate.

### Users and accounts on the four required systems

Core `User` already supports creation on OpenBSD, FreeBSD, NetBSD, and Rocky
Linux, plus additive supplementary memberships. Shell, primary group, login
class, and related settings are creation-only. `WithHome` is creation-only by
default. Core now also offers the explicit `WithManageHome` opt-in, which
converges an existing account's passwd home field without moving data. It is
in main, unreleased, as plan schema 19 (task s52; see [user.md](user.md)).
Until a release is pinned, the OpenBSD consumer keeps its `usermod -d` command
with an AWK guard. The prepared consumer migration is pending that release.

Keep existing behavior backward-compatible. Introduce explicit opt-in management
of selected existing attributes, starting with home, rather than changing what
`WithHome` means for every current client. A passwd home-field update is different
from moving home contents; never imply a move, deletion, recursive chown, password
reset, or membership removal. Specify locked/non-login accounts, create-home
permissions, primary-group creation, and unsupported options per platform. Linux
`WithSystem` and BSD login classes must not be treated as interchangeable flags.

An optional `ServiceAccount` convenience resource can compose these operations
once this contract is stable. Runtime directories under `/var/run` need a reboot
lifecycle solution separate from account creation. No account deletion or
destructive reconciliation is included in this plan.

## External templates and consumer layout

Suggested layout, retaining one Go module per repository:

```text
conf/gonf/
  cluster/             SSH inventory and typed per-host values
  tasks/               explicit setup/action composition
  frontends/
    web.go             resource declarations, not config text
    maildns.go
    monitoring.go
    data.go            service topology and render data
    assets/
      httpd.conf.tmpl
      relayd.conf.tmpl
      smtpd.conf.tmpl
      nsd.conf.tmpl
      key.conf.tmpl
      rsyncd.conf
      zones/*.zone.tmpl
      acme-client.conf.tmpl
      acme.sh.tmpl
```

Move HTTPD/relayd's large raw constants and `appendf` loops first. Move SMTPD and
NSD next, eliminating the Perl-fragment replacement and comment-based splice in
`replaceLegacyF3SZoneLoop`. Keep native zone syntax; do not make a person encode
every DNS record as nested Go constructors. Move static service allowlists and
rsync config out of Go where they are naturally operator-edited files.

ACME and Garage already demonstrate `.tmpl` plus `WithTemplateData`. Reuse that
facility; a generic template API is not missing. Current templates render on the
destination from packaged source/data, whereas frontend Go renderers run on the
controller. Preserve that distinction during extraction: either use explicit
data-only deterministic templates or add an explicit controller-render mode.
Do not silently expose new target environment/fact dependencies.

Use strict missing-key handling, small typed data structures, and no secret
lookups/network calls inside template functions. Retain plain files for static
configuration. For Gogios, keep JSON serialization rather than hand-writing
comma-sensitive JSON templates. Use a typed top-level configuration and small
check builders; a static external check catalogue is worthwhile only for truly
static definitions. Dependency semantics belong in reviewed data/builders.

Move shared assets only after inventorying their remaining Rex, Justfile, and
script users. Some `.tpl` files copied today contain no Perl directives; rename
them to plain files rather than passing them through unnecessary rendering.
Do not move unrelated f3s assets merely to make a uniform directory tree.

Human-editability acceptance examples:

- Add a website or Garage bucket by editing one topology entry; DNS, certificate
  names, routing, and checks derive consistent values without repeated edits.
- Change an HTTPD directive in an HTTPD template, not a Go format string.
- Change a schedule in inventory without editing host-selection boilerplate.
- Add a validated daemon config without writing staging paths or dependency slices.
- Add a non-login account without knowing four different command-line tools.
- Rotate a secret by changing its provider value; recipe code and logs expose
  only its logical reference.

## Secrets: recommended foostore integration

### What exists and what is missing

Foostore's code now defaults to KeePass, despite the README still describing
AES-CBC as the default. Relevant code:

- `internal/cli/cli_backend.go`: KeePass/legacy backend creation and credential
  loading. `$PIN`, a configured password file, or an interactive prompt are used.
- `internal/cli/cli_dispatch.go`: `get` is an alias for search-and-cat; it uses
  regex lookup and prints matching index descriptions. No match can return
  success. `FOOSTORE_SHELL` can force shell mode.
- `internal/keepass/keepass.go`, `search.go`, and `format.go`: text entries become
  formatted Password/User/URL/Notes blocks, not raw values of one selected field;
  binary cat is skipped with a message rather than returned as raw bytes.
- `internal/crypto/crypto.go`: the legacy backend reuses a PIN-derived IV and
  AES-CBC without an authentication tag. Do not recommend that format for new
  infrastructure secrets merely because it is encrypted.

Prefer KeePass integration after validating the database format/version and
automation threat model. This review did not inspect the user's actual database,
password file, backend selection, permissions, backups, or unlock setup and is
not a blanket security endorsement.

### Required machine-facing contract

Add a separate, read-only foostore command, conceptually:

```text
foostore read --exact --raw --non-interactive <reference>
```

The exact syntax is a proposal. It needs:

1. Stable exact entry identity plus explicit field or attachment selection. A
   path/title alone can be ambiguous; reject duplicates instead of choosing one.
2. Stdout containing only exact requested bytes, including intentional trailing
   newlines and binary data. No headings, formatting, status messages, or search
   results. No parsing the current human-oriented Password/Notes format.
3. Distinct not-found, ambiguous, locked/authentication, corrupt-store, and I/O
   failures. Gonf optionality may suppress only not-found.
4. No prompt, shell, fzf, editor, clipboard, export file, implicit import, or git
   synchronization. Reject inherited interactive overrides in this mode.
5. Bounded execution/cancellation and sanitized errors. Unknown backend names
   should fail rather than silently falling through to the legacy backend.
6. An explicit unlock strategy: a secure inherited file descriptor or existing
   credential agent is preferable to passwords in argv or a globally exported
   `PIN`. A mode-0600 password file can be an explicit local tradeoff, not an
   unexplained default; define unattended and operator-driven use separately.

Keep encrypted-store writes, backup/sync, and server-side credential rotation
outside Gonf apply. Reading the latest secret is not authorization to rotate a
Garage cluster's shared RPC identity or revoke other credentials.

### Gonf side

Status (z52): the resolver contract exists — package `secret` (`Provider`,
typed `*secret.Error` kinds, the compatible `FileProvider`, `Snapshot`) with
`api.SetSecretProvider` / `api.ResolveSecret`; see `docs/secrets.md`. Typed
references in file content and template data, sensitivity in plans, and the
foostore adapter remain open (062, 162).

Configure a provider once at the consumer composition root. Use a small
context-aware resolver contract returning bytes and typed errors. Integrate
foostore through argv-based subprocess execution first; its current implementation
is in Go `internal` packages and should not be imported directly into gonf.
Keep the provider implementation separate from recipe/resource packages.

Preserve `MustSecret`/`OptionalSecret` compatibility for the file provider. Add a
typed secret-reference/content path for new usage so sensitivity survives file
content and template data. A provider interface alone cannot recover sensitivity
after a secret has been concatenated into an ordinary string.

Conceptual consumer syntax, not an existing API:

```go
SecretFile("/etc/goprecords-upload.token",
    SecretRef("frontends/fishfinger/goprecords/token"))
```

For NSD/Garage, support a secret reference within structured template data without
requiring consumer-side subprocess or interpolation code. Resolve once for a
defined plan snapshot and selected targets; do not let different chunks fetch
different versions mid-deployment. No store database or unlock credential belongs
on the managed host.

Handle the complete lifecycle:

- A redacted human preview is distinct from an executable plan. Secret-bearing
  plans must not default to printable stdout; explicit export needs a documented
  sensitive-artifact policy. Do not label redacted output replayable.
- Transport/apply may materialize plaintext where necessary. Keep memory-backed
  transfer and SSH; secure temporary files, candidate validation, and destination
  ownership. Audit mixed-privilege blob staging as well as single-chunk plans.
- Initially allow executable secret-bearing plans only with existing private
  filesystem protections and an explicit retention policy. If durable artifacts
  are required, design recipient encryption or a protected sidecar before claiming
  encrypted plans; that is separate work, not implied by foostore.
- Redact secret bytes from logs, diffs, validator failures, and error paths. Avoid
  logging hashes of low-entropy secrets. Memory zeroization in Go cannot be
  guaranteed merely by changing strings to byte slices.
- Use explicit provider mapping during transition. Do not silently fall back to
  stale local files when foostore is locked or broken. Missing optional tokens
  need an explicit keep/disable/remove policy for already configured hosts.
- Retain legacy secret copies until equality, recovery, and all consumers have
  been verified; subsequently remove only individually identified obsolete copies
  with separate authorization. Never commit payloads or unencrypted fixtures.

## Rex retirement status and closure plan

**No, the repositories are not fully cleaned up.**

| Current tracked Rexfile | Disposition |
| --- | --- |
| `conf/Rexfile` | Still loads legacy frontend/playground and r-node files |
| `conf/frontends/Rexfile` | Still contains operational tasks overlapping Gonf |
| `conf/f3s/r-nodes/Rexfile` | Two tasks overlap the new rnodes package |
| `conf/playground/Rexfile` | Cron canary; candidate for explicit retirement |
| dotfiles | No tracked Rexfile remains, but Fedora still installs the `Rex` package |
| `conf/f3s/garage/Rexfile` | Already removed; Justfile deployment uses Gonf |

The gap audit already explicitly excludes `id`, `dump_info`, cron canaries, and
disabled Gorum deployment. Keep that distinction: do not implement or enable them
simply to make a migration table say “done.” The account-only Gorum task does not
mean the daemon is migrated. ACME invocation and IRC bouncer remain explicit
tasks outside the frontend setup aggregate by design.

Retirement sequence:

1. Create a final old-task → replacement/retired mapping, including called assets,
   first-install prerequisites, and intentionally changed behavior. Resolve the
   dangling tmux helper and aggregate documentation.
2. Complete correctness fixes and compare rendered configuration, schedules,
   dependencies, ownership, and failure behavior. Compilation and plan recording
   are insufficient closure evidence.
3. With later deployment authorization, validate on representative canaries and
   verify a second apply, service recovery, and failover behavior. Choose one
   configuration owner per service during cutover; prevent old `rex commons`
   from silently reasserting old state.
4. Update active runbooks and automation first. Known stale references include
   `conf/frontends/README.md`, parts of `frontends/AGENTS.md`, f3s Forgejo docs/chart
   comments, shuriken chart comments, r-node unit comments, dotfiles README, and
   the canonical gap document. Preserve clearly labeled historical explanations.
5. Remove the four remaining Rexfiles only after their mappings are accepted.
   Remove Perl templates once no consumer still reads/translates them. Remove
   the dotfiles Rex package only after checking non-Gonf workflows and tooling.
   Do not delete the `rex` SSH account or rename SSH inventory just because the
   configuration engine changed.
6. Search tracked files and hidden CI/config for active Rex invocations and Perl
   template directives. Allowlist historical references; verify public task names
   and runbooks match the new CLI. Secret copies have their own retirement gate.

## P0 baseline — consumer ownership and compatibility

Recorded for task `b52` on 2026-09-20. This is an exact current-source baseline
for later migration work. It intentionally records known defects and awkward
ownership boundaries rather than normalizing task names or behavior. The
commands below only built, listed, or recorded plans; they did not contact a
host, apply a plan, request a certificate, or use a real secret.

### Public task surface

The following names are the output of each consumer's `go run ./cmd/gonf
-list`. Aggregates are included because they are public CLI inputs. Preserve
these names until a separately approved compatibility change says otherwise.
The operational descriptions remain in the consumer registrations and must be
reviewed when a task's behavior changes; this P0 inventory establishes the
stable input-name baseline rather than duplicating 78 descriptions.

| Consumer | Module pin | Registered public names |
| --- | --- | --- |
| conf | `github.com/snonux/gonf v0.14.0` | 52 |
| dotfiles | `github.com/snonux/gonf v0.12.2` | 26 |

```text
conf: freebsd freebsd_cron freebsd_newsyslog freebsd_packages freebsd_script
freebsd_services freebsd_stamp_dir frontends frontends_acme
frontends_acme_invoke frontends_base frontends_cron frontends_d_tail
frontends_dns_failover frontends_foostats frontends_gemtexter frontends_gogios
frontends_goprecords frontends_httpd frontends_inetd frontends_irc_bouncer
frontends_myname frontends_newsyslog frontends_nsd frontends_pf frontends_ping
frontends_pkg_repo frontends_relayd frontends_rsync frontends_script
frontends_service_accounts frontends_services frontends_smtpd frontends_uptimed
frontends_wire_guard_hosts garage garage_config pis_netbsd pis_netbsd_cron
pis_netbsd_newsyslog pis_netbsd_script pis_netbsd_services rnodes
rnodes_nfs_mount_monitor rnodes_persistent_journal rocky rocky_gonf_link
rocky_logrotate rocky_packages rocky_script rocky_stamp_dir rocky_units

dotfiles: home home_agents home_bash home_calendar home_fish
home_fish_completions home_ghostty home_gitconfig home_gitsyncer home_helix
home_hexai home_lazygit home_opencode home_pipewire home_prompts home_quickedit
home_scripts home_signature home_ssh home_sway home_systemd_user
home_taskwarrior home_timesamurai home_tmux home_vale pkg_fedora
```

### Inputs, ownership, and first-install boundaries

| Consumer area | Public inputs and owner | Compatibility and first-install boundary |
| --- | --- | --- |
| conf composition | `cmd/gonf/main.go` calls `cluster.Register` and `tasks.Register`; `tasks/tasks.go` owns prefixes and aggregate membership; `cluster/cluster.go` owns hosts, clusters, SSH/privilege facts, and typed values. | Keep task names, cluster names, privilege chunks, and inventory values stable. A task may rely on a destination-side `WhenHostname` guard even when the controller runs on another OS. |
| conf frontend configuration | `frontends` owns topology, controller-rendered configuration, source assets under `~/git/conf/frontends`, and the controller-relative `secrets/` inputs. Gonf owns a ported task only after Rex stops writing the same live state. | `frontends` deliberately excludes `frontends_acme_invoke` and `frontends_irc_bouncer`; certificate requests and the existing ZNC deployment remain explicit actions. NSD/PF validation, cron adoption, and DNS publication have known gaps F2–F4 and must retain their current names while corrected. |
| conf OS, r-node, and Garage recipes | The OS packages own unattended-update declarations; `rnodes` owns the two systemd maintenance recipes; `garage_config` owns only Garage TOML plus service convergence. | `garage_config` assumes the package, `garage` group, data/metadata directories, and an initialized cluster already exist. It requires `secrets/garage/rpc_secret`; it is configuration deployment, not first-host provisioning. |
| dotfiles composition | `cmd/gonf/main.go` registers `home_*`, profile-gates `pkg_fedora`, and defines the `home` regex aggregate. Sources resolve below `~/git/dotfiles`; optional private input resolves below `~/git/conf_private/dotfiles`. | Keep `home_prompts` as the public legacy alias for `home_agents` until the alias task can preserve compatibility without duplicate aggregate expansion. `pkg_fedora` is the Fedora profile entry point and needs its privilege contract corrected separately. |
| plan and secret inputs | `MustSecret` and `OptionalSecret` resolve only regular files beneath the invoking consumer's `./secrets` directory. File/template assets may be packaged as blobs; host values and template data are public record-time inputs. | Secret values enter executable plan material today. The P0 tests used disposable synthetic values only. Do not infer encrypted plans, external-provider support, native validation, or a production preview from successful recording. |

The intended Rex differences are also compatibility requirements: keep the
explicit ACME and IRC actions out of ordinary frontend setup; retain the
disabled Gorum account-only task; do not restore the retired Garage Rexfile;
and do not treat the missing legacy `tmux-edit-send` asset as a completed port.
Rex remains the owner of every unported task, while a ported task needs one
effective writer during cutover.

### Reproducible non-applying compatibility evidence

The checked-out core identified itself as v0.14.1 with plan schema 16. Both
consumers first passed `go build ./...`, `go test ./...`, `go vet ./...`, and
`go run ./cmd/gonf -list` using their released pins. The task lists above were
identical when the same consumers were built in a temporary Go workspace that
selected the local core checkout; neither `go.mod` nor `go.sum` changed.

The following is the compact reproduction recipe used for the local-core
recordings. It deliberately uses detached worktrees, synthetic inputs, and
temporary output paths; replace `~/git` only if the three repositories live
elsewhere. `wc -l` counts JSONL operations in the recorded plan.

```sh
root=$(mktemp -d)
git -C ~/git/gonf worktree add --detach "$root/gonf" HEAD
git -C ~/git/conf worktree add --detach "$root/conf" HEAD
git -C ~/git/dotfiles worktree add --detach "$root/dotfiles" HEAD
(cd "$root" && go work init "$root/gonf" "$root/conf/gonf" "$root/dotfiles/gonf")
export GOWORK="$root/go.work"
mkdir -p "$root/conf/gonf/secrets/frontends/var/nsd/etc" "$root/conf/gonf/secrets/garage"
printf '%s\n' synthetic-nsd-key > "$root/conf/gonf/secrets/frontends/var/nsd/etc/nsd_key.txt"
printf '%s\n' synthetic-garage-rpc-secret > "$root/conf/gonf/secrets/garage/rpc_secret"
(cd "$root/conf/gonf" && go run ./cmd/gonf plan -o "$root/nsd" frontends_nsd && test "$(wc -l < "$root/nsd/plan.jsonl")" = 53)
(cd "$root/conf/gonf" && go run ./cmd/gonf plan -o "$root/garage" garage_config && test "$(wc -l < "$root/garage/plan.jsonl")" = 13)
(cd "$root/dotfiles/gonf" && go run ./cmd/gonf -profile fedora plan -o "$root/pkg" pkg_fedora && test "$(wc -l < "$root/pkg/plan.jsonl")" = 42)
(cd "$root/dotfiles/gonf" && go run ./cmd/gonf plan -o "$root/agents" home_agents && test "$(wc -l < "$root/agents/plan.jsonl")" = 22)
git -C ~/git/gonf worktree remove "$root/gonf"
git -C ~/git/conf worktree remove "$root/conf"
git -C ~/git/dotfiles worktree remove "$root/dotfiles"
rm -rf "$root"
```

Within that disposable workspace, synthetic regular files were supplied only at
`secrets/frontends/var/nsd/etc/nsd_key.txt` and `secrets/garage/rpc_secret`.
The following `plan -o` recordings succeeded without applying them:

| Consumer command | Recorded operations | What it covers |
| --- | ---: | --- |
| conf `frontends_nsd` | 53 | controller secret, zone/config rendering, candidate validation dependency graph, privileged live files, and restart gate |
| conf `garage_config` | 13 | synthetic RPC secret in template data, per-host Garage data, privilege, and service change gate |
| dotfiles `-profile fedora pkg_fedora` | 42 | profile-selected package task and its public name |
| dotfiles `home_agents` | 22 | optional controller sources and home symlink declarations |

`plan` records and writes a private temporary artifact; it does not execute
the recorded operations. No claim of native OpenBSD/FreeBSD/NetBSD/Rocky
validation follows from this evidence. The disposable workspace, plans, and
synthetic files are removed after verification.

## Proposed implementation sequence (not started)

Every work package below inherits this mandatory compatibility gate:
**all existing gonf tests and all existing gonf clients in both dotfiles and conf
must still work after the change**. Preserve API/plan compatibility, task names,
and intentional operational behavior; document approved corrections separately.
No work package is complete on compilation alone. New core behavior tests belong
in gonf, not `conf/gonf/*_test.go`, per conf's AGENTS.md.

| Order | Work package | Dependencies and completion evidence |
| --- | --- | --- |
| P0 | Baseline and ownership map | Capture current task inventory, source inputs, pins, representative non-applying synthetic-secret plans, and intended Rex differences; identify live validation gates |
| P1 | Correctness repairs | F2–F8; native config validation, cron migration fixtures, role dependency checks, privilege/OS tests, and explicit DNS writer decision; no broad refactor mixed in |
| P2 | Core validation and safe config publication | P1 fixtures; single-file first, then SMTPD/config-set use case; no live replacement on failed validation, no accidental restart, retry/concurrency tests |
| P3 | Core Cron adoption and small account extension | Preserve current marker format and additive account defaults; four-platform account command/state fixtures; remove corresponding consumer shell bookkeeping |
| P4 | Extract native templates and simplify frontend data | P1/P2; compare outputs semantically, eliminate Perl translation and format-string config assembly; keep JSON safely serialized |
| P5 | Host iterator, task aliases, explicit aggregate membership | P0; preserve names, privilege chunks, destination guards, and selected-host inputs; eliminate double alias expansion and opaque remote predicates |
| P6 | Systemd units and BSD login-class composition | Reuse existing low-level resources; migrate rnodes and dotfiles user units; preserve unit/drop-in contents and restart/watch semantics |
| P7 | Foostore exact machine read and unlock contract | Independent of cosmetic refactors; synthetic KeePass/attachment fixtures, ambiguity/missing/locked/error tests, clean stdout, no interactive or write side effects |
| P8 | Gonf provider and sensitivity propagation | P7 plus plan-lifecycle design; fake provider tests, file-provider compatibility, redaction/export/transport tests, explicit version/schema handling |
| P9 | Consumer secret cutover | P8; retain logical paths via mapping, verify byte equality without output, controlled rotation/recovery exercise; no automatic deletion of legacy copies |
| P10 | Release and both-client compatibility sweep | Test released pins and local new core via a temporary workspace, then deliberate consumer upgrades; task/plan/output checks across all supported profiles |
| P11 | Authorized canaries and Rex retirement | P1–P10 as applicable; native validators, two-run convergence, failover/reboot checks, runbook switch, then exact-target removals with rollback path |

P7 can proceed independently while P1–P6 are developed; neither authorizes
production credential access. DNS publication may need a separate implementation
step after its ownership decision. Avoid blocking every readability improvement
on a generalized DNS engine or encrypted-plan feature.

### Cross-cutting implementation rules

- Keep public registration semantics: every public `Have*` registers its resource;
  concrete dependencies use `embed.DependsOn`, expand `Multi` dependencies, and
  flow through `resource.Register` and the public plan engine. New universally
  shared state belongs in `embed`, not copied into every resource.
- Give new resource kinds/options record → encode → decode → apply coverage,
  local/remote parity, supported-version handling, and meaningful change reports.
  Prefer composition of existing operations until a new operation is justified.
- Test fresh install, already-converged state, unmanaged legacy state, drift,
  missing inputs, invalid config, absent services, failure/retry, and concurrency.
- Do not infer an “unchanged packages” guarantee from current `IsLatest` behavior:
  some backends intentionally run upgrades and report changes. File/config
  no-change and service restart gates need separate assertions.
- Preserve supported behavior already present: `WithTemplateData`, additive User
  creation, `SystemdTimer`, legacy `DependsOn + IfChanged` daemon reload, nested
  task recording, and special file-mode normalization do not need reinvention.
- Verify account creation/opt-in updates on OpenBSD, FreeBSD, NetBSD, and Rocky;
  verify dotfiles on its declared Fedora/Linux/FreeBSD paths. Native VM/canary
  validation requires separate authorization and must not be implied by mocked
  command tests.
- Measure simplification by removed staging/cron/account boilerplate and the
  human-editability examples above, not just reduced line count. Keep small,
  explicit Go APIs, typed data, and ordinary templates; avoid generic frameworks
  that move complexity into less discoverable configuration.

## Coverage inventory

| Consumer source | Review focus |
| --- | --- |
| dotfiles `cmd/gonf/main.go`, `paths/paths.go`, `Magefile.go`, wrapper, README | Registration, profile selection, roots, build/usage contract |
| dotfiles `tasks/home.go` | Every home task, alias, optional input, guards, sync/link semantics, user units |
| dotfiles `tasks/pkg.go` | Package list, privilege, retained Rex dependency |
| conf `cmd/gonf/main.go`, `tasks/tasks.go`, `cluster/cluster.go`, `paths/paths.go` | All registrations/aggregates, inventory, host values, asset and secret roots |
| conf `frontends/data.go` | Topology and role data shared by renderers |
| conf `frontends/maintenance.go` | Base, accounts, hostnames/hosts, uptime upload, rsync, Gemtexter, ACME, IRC |
| conf `frontends/web.go` | HTTPD, inetd, relayd, PF, validation helper, all raw render fragments |
| conf `frontends/maildns.go` | SMTPD tables, key/config/zone candidates, publication, failover cron |
| conf `frontends/monitoring.go` | Custom packages, account workaround, Gogios generation/dependencies, cron, Foostats |
| conf `openbsd`, `netbsd`, `freebsd`, `rocky` unattended files | Scripts, services, schedules, logs, package prerequisites, OS boundaries |
| conf `rnodes/maintenance.go` | Unit/drop-in repetition, watch sets, persistent journal behavior |
| conf `garage/garage.go` | Template data, secret encoding, service gating, provisioning prerequisites |

End state: recipes say **what the host should have**; reusable gonf resources own
safe convergence mechanics; templates contain native config; topology is edited
once; and secrets are referenced, not manually read or formatted in task bodies.

## Task register — created 2026-09-20

Created with the agent-task-management skill and `ask`, under project `gonf`.
Every new task carries `+agent +gonfmigration +dslreview`, source references,
acceptance criteria, workflow instructions, and the requirement that all gonf
tests and all clients in both dotfiles and conf still work. Task annotations name
the actual implementation repositories; centralized tracking does not limit the
specified fixes to the core repository. Task `b52` records the P0 baseline
above; the remaining work packages stay separately scoped.

Inspect the batch from `~/git/gonf` with:

```sh
~/go/bin/ask list +gonfmigration +dslreview sort:priority-,urgency-
~/go/bin/ask info <alias>
```

| Task alias | Scope | Plan reference |
| --- | --- | --- |
| `b52` | Record consumer DSL review baseline and ownership/compatibility matrix | P0; F9; Coverage inventory |
| `c52` | Fixed NSD directive whitespace in rendered frontend configuration | F1; P1 (complete) |
| `d52` | Specify one DNS publication owner and failover-safe SOA serial contract | F2; P1 |
| `e52` | Implement failover-safe DNS publication and stable-on-no-change SOA serials | F2; P1 |
| `f52` | Add fail-closed opt-in legacy-job adoption to the core Cron resource | F3; P3 |
| `g52` | Replace frontend cron surgery and adopt NSD, rsync, PF and Gogios legacy jobs | F3; P1/P3 |
| `h52` | Add safe single-file candidate validation to Gonf file resources | F4; P2; Validation resource contract |
| `i52` | Add a validated multi-file ConfigSet with member-level change handles | P2; F4; Validation resource contract |
| `j52` | Migrate frontend validation to core resources and validate PF before live replacement | F4; P2 |
| `k52` | Restore master-role dependencies in generated Gogios HTTP and TLS checks | F5; P1 |
| `l52` | Declare Fedora package privilege and repair dotfiles platform/remote task guards | F6; P1 |
| `m52` | Fix ACME skip/change reporting and align requested certificates with configured domains | F7; P1 |
| `n52` | Add first-class task aliases and explicit safe aggregate membership | F8; P5; Host/task readability |
| `o52` | Add a typed current-cluster host iterator with destination-side guards | P5; Host/task readability |
| `p52` | Simplify consumer host loops and task aliases and fail visibly on broken optional sources | F8; P5 |
| `q52` | Preserve wrapper argv and centralize portable controller asset roots in both clients | F8; P5 |
| `r52` | Add narrow keyed/shared-file editing and adopt the legacy PKG_PATH setting | F8; Core-pattern table |
| `s52` | Add explicit opt-in existing-account home management on OpenBSD FreeBSD NetBSD and Rocky | Users and accounts; P3 |
| `t52` | Compose BSD login-class file installation and database rebuild as a core resource | P6; Core-pattern table |
| `u52` | Move frontend HTTPD relayd and static service configuration out of Go strings | P4; External templates |
| `v52` | Replace SMTPD and NSD Go rendering and Perl-fragment translation with native templates | F1/F2; P4 |
| `w52` | Simplify Gogios config data and derive consistent site routing/certificate/check policy | P4; Human-editability acceptance |
| `x52` | Add reusable existing-systemd-unit composition and migrate rnodes and dotfiles units | P6; Core-pattern table |
| `y52` | Add exact raw noninteractive read-only foostore lookup for Gonf secrets | P7; Secrets required machine-facing contract |
| `z52` | Add a typed Gonf secret-provider contract preserving file-provider compatibility | F10; P8; Gonf side |
| `062` | Carry secret sensitivity through plans templates previews validators and transport | F10; P8; Secrets lifecycle |
| `162` | Add an argv-based foostore secret adapter outside Gonf recipe bodies | P7/P8; Gonf side |
| `262` | Migrate conf secret declarations to typed provider references with explicit cutover policy | F10; P9 |
| `362` | Separate non-mutating remote preview from Gonf runtime bootstrap | F10; P1/P8 |
| `462` | Make consumer setup prerequisites explicit and prepare accurate Rex retirement mappings | F9; P11; Rex retirement status |
| `562` | Final release/pin and both-client compatibility sweep | P10 |
| `662` | Audit closure: all findings, native evidence and Rex retirement | All findings; P11 |
| `762` | Final audit marker after closure | Depends on `662` only |

Existing tasks reused rather than duplicated:

- Dotfiles `n42`: earlier release adoption; now tagged into this batch. It is an
  external hard gate, not a dependent of the later compatibility sweep.
- Gonf `y42`: native four-OS verification; now depends on `562` and carries
  explicit authority/evidence requirements.
- Conf `v42`: final Rex retirement; annotated with the new findings and external
  hard gates `562`, `y42`, and `n42`. Its pre-existing started state was
  preserved; work was not resumed. Closure `662` must also confirm `v42` is
  completed, so the final marker cannot imply retirement happened prematurely.

Cross-project gates are explicitly checked using `ask` in their owning
repositories; do not assume a scoped alias creates a cross-project dependency.
The skill-required local start marker is `audit/2026-09-20` on gonf `c540390`.
It was not pushed and is not a completion marker. Task `762` owns moving
that exact tag to the post-fix commit after verification, with user approval for
any push. No production actions or secret access were performed when filing tasks.
