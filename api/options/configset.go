package options

import resourceoptions "github.com/snonux/gonf/resource/options"

// ConfigSetOption configures api.ConfigSet. See resource/options for the
// full contract and docs/design/config-set.md for the publication guarantees.
type ConfigSetOption = resourceoptions.ConfigSetOption

// Capability interfaces of the config-set options.
type (
	MemberAddable  = resourceoptions.MemberAddable
	SetValidatable = resourceoptions.SetValidatable
	Chrootable     = resourceoptions.Chrootable
	StagingDirable = resourceoptions.StagingDirable
)

// Member-path token parts; recipes use MemberPath and MemberChrootPath.
const (
	MemberPathTokenPrefix       = resourceoptions.MemberPathTokenPrefix
	MemberChrootPathTokenPrefix = resourceoptions.MemberChrootPathTokenPrefix
	MemberTokenSuffix           = resourceoptions.MemberTokenSuffix
)

// Config-set options and member-path placeholders.
var (
	// ConfigFile adds one member file to a config set.
	ConfigFile = resourceoptions.ConfigFile
	// WithSetValidation adds an argv validator run against the complete
	// staged set before any member is published.
	WithSetValidation = resourceoptions.WithSetValidation
	// WithChroot declares the daemon's chroot; members and staging must lie
	// below it and MemberChrootPath renders relative to it.
	WithChroot = resourceoptions.WithChroot
	// WithStagingDir selects the directory receiving the private staging
	// directory (default: the members' deepest common directory).
	WithStagingDir = resourceoptions.WithStagingDir
	// MemberPath is the typed placeholder for a member's absolute path
	// (staged while validating, live when published).
	MemberPath = resourceoptions.MemberPath
	// MemberChrootPath is MemberPath relative to the WithChroot directory.
	MemberChrootPath = resourceoptions.MemberChrootPath
)
