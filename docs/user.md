# User resource

`User` ensures a local account exists on Rocky Linux, OpenBSD, FreeBSD, or
NetBSD. It is intentionally additive-only: gonf never deletes an account or a
group, removes a supplementary membership, or rewrites an existing account's
primary group, home, shell, login class, or system-account setting.

```go
User("_dserver",
    WithPrimaryGroup("_dserver"),
    WithUserGroup("wheel"),
    WithSupplementaryGroups("audio", "video"),
    WithHome("/var/run/dserver"),
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

`WithGroup` remains accepted as a compatibility spelling for the creation-time
primary group, and `WithClass` aliases `WithLoginClass`; prefer the explicit
user option names above in new recipes.

The resource is carried through the plan schema as a `user` operation, so the
same additive-only behavior applies to local `Apply`, recorded plans, and
remote push/apply.

See also: [options.md](options.md), [plan.md](plan.md), and the
[documentation index](README.md).
