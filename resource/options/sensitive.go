package options

// Sensitivable is the capability of the WithSensitive option: mark the
// resource's payload as secret material (embed.Sensitivity implements it,
// and a config-set member implements it by marking its whole set).
type Sensitivable interface{ SetSensitive() }

// SensitiveOption is the family set WithSensitive belongs to: the resource
// kinds whose recorded op carries a payload that can hold secret material
// (file content, a synced directory tree, config-set members, a command's
// argv and environment, a package operation's environment, a cron line, a
// systemd timer's ExecStart command). The kinds whose ops carry only
// identities and metadata (Link, Service, Timer, DaemonReload, LocalUser)
// deliberately do not accept it, so marking one of them is a compile-time
// error rather than a no-op.
type SensitiveOption interface {
	FileOption
	DirOption
	PackageOption
	CronOption
	SystemdTimerOption
	CommandOption
	ConfigSetOption
}

// sensitiveOption is WithSensitive's concrete type; its marker methods
// (below) admit it into exactly the SensitiveOption families.
type sensitiveOption func(any)

// WithSensitive declares that the resource's payload holds secret material
// the plan's secret scan cannot recognise on its own: a value transformed
// beyond trimming (base64-encoded, hashed, split or case-changed), a secret
// that never went through ResolveSecret, or a synced directory tree
// (SyncDir, Dir with a source), which the scan does not read. The recorded
// op is then marked sensitive (plan.Op.Sensitive, plan schema 22) exactly
// like an op the scan matched, with every consequence documented in
// docs/secrets.md: `gonf plan -stdout` refuses it, `-redacted` withholds
// every payload string of the op (argv, environment, lines, content, ...),
// and the destination withholds validator, template, command and
// package-manager failure details and a command's argv from its logs.
// A Command marked with it must also have WithName (its unnamed ID would
// be its argv); cmd.Present refuses it otherwise.
//
// It only ever adds sensitivity (an op the scan matched stays sensitive
// without it) and changes nothing else about the op, so a plan recorded
// without WithSensitive is byte-for-byte what it was before the option
// existed. It is accepted on ConfigFile members too, where it marks the
// whole config set (one op carries every member).
var WithSensitive = sensitiveOption(func(target any) {
	requires(target, "WithSensitive", func(r Sensitivable) { r.SetSensitive() })
})

// Apply runs the option against target (see requires).
func (o sensitiveOption) Apply(target any) { o(target) }

// Marker methods admitting sensitiveOption into the SensitiveOption families.
func (sensitiveOption) fileOption()         {}
func (sensitiveOption) dirOption()          {}
func (sensitiveOption) packageOption()      {}
func (sensitiveOption) cronOption()         {}
func (sensitiveOption) systemdTimerOption() {}
func (sensitiveOption) commandOption()      {}
func (sensitiveOption) configSetOption()    {}
