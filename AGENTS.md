# Agent Guidelines

## Resource Management
All public functions which start with `Have` should be responsible for registering the resource they create.

## Dependencies
Concrete resource types (e.g. `file.File`, `dir.Dir`, `link.Link`, `pkg.Package`)
embed `embed.DependsOn` to accumulate dependency IDs. The `DependsOn` option
records those IDs, and each `Present` function forwards them into
`resource.Register(..., x.DependsOn.IDs...)`. A `Multi` dependency is expanded
via `Dependencies()` so each member is depended upon individually, and
  The public `api.Apply()` snapshots registered drafts and uses the plan
  engine's dependency ordering. The lower-level `resource.Apply()` repository
  path remains only for tests and compatibility, and is deprecated.

## Shared embeds
State common to all concrete resource types lives in the `embed` package and is
embedded rather than redeclared:
- `embed.DependsOn` — dependency IDs (`IDs` field) plus the `AddDependency` method.
- `embed.Absence` — the `Absent` field plus the `SetAbsent` method (implements
  `opt.Absentable`).

When adding a field or capability shared by every resource type, prefer a new
embed type here instead of duplicating the field and its setter in each resource.
