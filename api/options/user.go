package options

import resourceoptions "github.com/snonux/gonf/resource/options"

// HomeManageable is implemented by resources accepting WithManageHome.
type HomeManageable = resourceoptions.HomeManageable

// WithManageHome opts a User in to converging an existing account's passwd
// home field to WithHome, without moving or creating the directory. See
// resource/options.WithManageHome and docs/design/user.md for the full contract.
var WithManageHome = resourceoptions.WithManageHome

// ShellManageable is implemented by resources accepting WithManageShell.
type ShellManageable = resourceoptions.ShellManageable

// WithManageShell converges an existing account's login shell to WithShell.
// WithShell remains creation-only unless this option is set.
var WithManageShell = resourceoptions.WithManageShell
