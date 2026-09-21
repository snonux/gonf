package options

// ConfigSetOption configures a config-set resource (api.ConfigSet). It is a
// resource family of its own so single-file options (WithValidation,
// WithLine, ...) cannot be passed to a set by accident; DependsOn is accepted
// through the allResourceOption marker declared below.
type ConfigSetOption interface {
	Apply(any)
	configSetOption()
}

// Capability interfaces used by the config-set options.
type (
	MemberAddable interface {
		AddMember(key, path string, opts []FileOption)
	}
	SetValidatable interface {
		AddSetValidator(bin string, args []string)
	}
	Chrootable     interface{ SetChroot(string) }
	StagingDirable interface{ SetStagingDir(string) }
)

type configSetOption func(any)

func (o configSetOption) Apply(target any) { o(target) }
func (configSetOption) configSetOption()   {}

// configSetOption on allResourceOption makes DependsOn a ConfigSetOption. It
// is declared here rather than in option.go so the config-set family stays
// self-contained.
func (allResourceOption) configSetOption() {}

// Member-path tokens. Like CandidatePath they contain NUL bytes, so they can
// never occur in a real path or collide with ordinary configuration text.
// resource/configset substitutes them; recipes build them with MemberPath and
// MemberChrootPath instead of spelling them out.
const (
	// MemberPathTokenPrefix starts a token that renders a member's absolute
	// path: the staged candidate during validation, the live path when
	// published.
	MemberPathTokenPrefix = "\x00gonf-member-path:"
	// MemberChrootPathTokenPrefix starts a token that renders a member's
	// path relative to the set's WithChroot directory (with a leading "/"),
	// i.e. the path a chrooted daemon sees.
	MemberChrootPathTokenPrefix = "\x00gonf-member-chroot-path:"
	// MemberTokenSuffix ends both token kinds.
	MemberTokenSuffix = "\x00"
)

// MemberPath returns the typed placeholder for the absolute path of the
// config-set member key. Use it inside member content (an include line, a
// table path) and inside WithSetValidation arguments: gonf renders the staged
// candidate path while validating and the live path when publishing, so the
// validator checks the complete staged set rather than live neighbours.
func MemberPath(key string) string {
	return MemberPathTokenPrefix + key + MemberTokenSuffix
}

// MemberChrootPath is MemberPath rendered relative to the set's WithChroot
// directory, for configuration read by a daemon after chroot(2).
func MemberChrootPath(key string) string {
	return MemberChrootPathTokenPrefix + key + MemberTokenSuffix
}

// ConfigFile adds the member key, published at the absolute live path, to a
// config set. Its content comes from WithContent or WithSource (read once on
// the controller at record time); WithMode, WithOwner and WithGroup set the
// published file's attributes exactly as for File. Other file options are not
// supported on members. The key names the member's own handle
// (ConfigSetMember[set/key]) and its MemberPath placeholder.
func ConfigFile(key, path string, opts ...FileOption) configSetOption {
	return configSetOption(func(target any) {
		requires(target, "ConfigFile", func(r MemberAddable) { r.AddMember(key, path, opts) })
	})
}

// WithSetValidation adds a validator a config set runs, directly via argv and
// never through a shell, against its complete staged candidate set before any
// member is published. Validators run in the order given; every one must exit
// 0. Arguments may use MemberPath/MemberChrootPath placeholders.
func WithSetValidation(bin string, args []string) configSetOption {
	return configSetOption(func(target any) {
		requires(target, "WithSetValidation", func(r SetValidatable) { r.AddSetValidator(bin, args) })
	})
}

// WithChroot declares the chroot directory the configured daemon runs in.
// Every member and the staging directory must lie below it (so validators
// resolving chroot-relative paths see staged files inside the chroot), and
// MemberChrootPath renders paths relative to it.
func WithChroot(root string) configSetOption {
	return configSetOption(func(target any) {
		requires(target, "WithChroot", func(r Chrootable) { r.SetChroot(root) })
	})
}

// WithStagingDir selects the existing directory that receives a config set's
// private staging directory. It must be an ancestor of every member (so
// relative references between members resolve identically when staged) and
// satisfy the WithValidation candidate-parent ownership rules. The default is
// the members' deepest common directory.
func WithStagingDir(dir string) configSetOption {
	return configSetOption(func(target any) {
		requires(target, "WithStagingDir", func(r StagingDirable) { r.SetStagingDir(dir) })
	})
}
