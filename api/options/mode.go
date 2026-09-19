package options

import (
	"os"

	resourceoptions "github.com/snonux/gonf/resource/options"
)

// ModeToFlags converts raw octal special bits into Go FileMode flags.
func ModeToFlags(mode os.FileMode) os.FileMode { return resourceoptions.ModeToFlags(mode) }

// ModeToWire renders a mode in the canonical plan-wire format.
func ModeToWire(mode os.FileMode) string { return resourceoptions.ModeToWire(mode) }

func normalizeMode(mode os.FileMode) os.FileMode { return resourceoptions.NormalizeMode(mode) }
