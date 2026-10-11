# Gonfy's disposable DSL fixture

This recipe exercises `WithShellVars`, `RegisterGroup`, `Sysctls`,
`RequireControllerFile`, `OptionalControllerDir`, `WithExclude`,
`WithEnableOnly`, `WithManageShell`, and `WhenNotOS` through real CLI plans
and applies. `fixture_copy` needs `fixture_base`; its private postcards never
enter the plan's blobs. `excluded` must be skipped on `GONF_E2E_GOOS`.

Build and run the unprivileged, automatically cleaned verification:

```sh
go build -o /tmp/gonfy-fixture ./examples/dsl-e2e
go build -o /tmp/gonfy-receiver ./cmd/gonf
python3 examples/dsl-e2e/verify.py /tmp/gonfy-fixture /tmp/gonfy-receiver
```

The runner asserts actual file bytes, excluded package bytes, dependency
order, destination guard, dry-run without writes, repeat convergence,
included drift repair, excluded destination preservation, packaged contents
surviving source removal, optional-source absence, and missing/wrong-type source refusal without an output plan.
It uses a private temporary directory and synthetic postcards only.

> 🦫 **Gonfy says:** Pack the postcard, leave the private diary at home.

For remote verification, set these controller environment variables:

| Variable | Meaning |
|---|---|
| `GONF_E2E_SOURCE` | Source directory containing `welcome.txt` and optional `postcards/` |
| `GONF_E2E_DESTINATION` | Disposable absolute destination, default `/tmp/gonfy-dsl-e2e-l93` |
| `GONF_E2E_GOOS` | Destination GOOS for the deliberately false `excluded` guard; defaults to controller GOOS |
| `GONF_E2E_HOST`, `GONF_E2E_USER` | SSH destination; fixture uses port 22 |
| `GONF_E2E_RECEIVER` | Separate remote gonf binary path built from the revision under verification |
| `GONF_E2E_PRIVILEGE` | `doas`, `sudo`, or empty for no elevation |
| `GONF_E2E_DELIVERY` | `preview` or `push`; when empty, use the ordinary recipe CLI |
| `GONF_E2E_DRY_RUN` | `1` makes delivery dry-run |
| `GONF_E2E_SYSCTL`, `GONF_E2E_VALUE` | One explicitly selected live sysctl and its desired value |

For example, after staging the receiver at the separate path:

```sh
GONF_E2E_DELIVERY=preview /tmp/gonfy-fixture fixture_base excluded
GONF_E2E_DRY_RUN=1 GONF_E2E_DELIVERY=push /tmp/gonfy-fixture fixture_copy excluded
GONF_E2E_DELIVERY=push /tmp/gonfy-fixture fixture_copy excluded
GONF_E2E_DELIVERY=push /tmp/gonfy-fixture fixture_copy excluded
```

Strict preview refuses blob-backed `fixture_copy`, because staging its source
would violate strict preview's contract. Preview `fixture_base` and use a
regular dry-run push for the complete packaged fixture before applying it.

The privileged tasks are opt-in by explicit task name. `fixture_shell`
creates/manages only account `gonfy_e2e`, `fixture_enable` enables only service
`gonfy_e2e`, and `fixture_sysctl` needs `fixture_base` before persisting and
probing the selected setting. Create a disposable, inactive service definition
before invoking `fixture_enable`; verify it remains inactive after both
applies. Check the account and unit names are unused before starting, and
remove only the account, service definition and enablement created by this
fixture afterward. Read and preserve the original sysctl value before any
live-drift probe, and restore it even if verification fails. These tasks do
not belong in a production aggregate.

The [2026-10-11 verification record](RESULTS.md) lists exact revisions,
commands, offline profile coverage, live targets, summaries and limits.

`consumer-wrapper.py <consumer-main.go> <fresh-temporary-output.go>` creates
a delivery wrapper while retaining the consumer's registration and secret
provider. It never edits the input. Set `L93_DELIVERY=preview|push`,
`L93_HOST`, `L93_USER`, `L93_PRIVILEGE`, optional `L93_PORT=2`, and
`L93_DRY_RUN=1` for dry-run delivery. The generated wrapper uses the same
separate receiver path; the verification record gives build commands and
selected task IDs.
