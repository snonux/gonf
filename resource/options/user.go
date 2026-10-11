package options

// HomeManageable is implemented by resources that can opt in to converging an
// existing account's home field (the User resource). It lives beside, not in,
// the shared capability list in option.go so account-specific options stay
// grouped with the account contract they extend.
type HomeManageable interface{ SetManageHome() }

// WithManageHome opts a User in to converging the passwd home field of an
// account that already exists to the WithHome value. Without it, WithHome is
// only a creation-time attribute, which remains the default so existing
// recipes keep their additive-only behaviour.
//
// It rewrites only the home field (usermod -d / pw usermod -d without -m): it
// never moves, copies, creates, deletes, or chowns a directory, and never
// changes passwords, lock state, the shell, the login class, or memberships.
// Pair it with a Dir resource when the directory itself must exist. WithHome
// must be an absolute, clean path; a missing or malformed home is rejected at
// plan record time and before any destination command runs.
var WithManageHome = userAccountOption(func(target any) {
	requires(target, "WithManageHome", func(r HomeManageable) { r.SetManageHome() })
})

// ShellManageable is implemented by resources managing an existing login shell.
type ShellManageable interface{ SetManageShell() }

// WithManageShell opts a User in to converging an existing account's login
// shell to WithShell. Without it, WithShell remains creation-only. The shell
// must be a nonempty absolute, clean path without passwd separators or control
// characters. Only that field changes; passwords, locks, other account fields
// and home contents are retained. Missing accounts use normal account creation.
var WithManageShell = userAccountOption(func(target any) {
	requires(target, "WithManageShell", func(r ShellManageable) { r.SetManageShell() })
})
