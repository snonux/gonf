package options

import resourceoptions "github.com/snonux/gonf/resource/options"

// Explicit sensitivity (see resource/options WithSensitive and
// docs/secrets.md): the option, the family set that accepts it (File, Dir,
// Package, Cron, SystemdTimer, Command, ConfigSet and ConfigFile members)
// and its capability interface.
type (
	SensitiveOption = resourceoptions.SensitiveOption
	Sensitivable    = resourceoptions.Sensitivable
)

// WithSensitive marks a resource's payload as secret material the plan's
// secret scan cannot recognise (transformed values, synced trees).
var WithSensitive = resourceoptions.WithSensitive
