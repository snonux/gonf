package options

import (
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/logger"
)

const (
	rawSetuid os.FileMode = 0o4000
	rawSetgid os.FileMode = 0o2000
	rawSticky os.FileMode = 0o1000
	modeMax   os.FileMode = 0o7777
)

// ModeToFlags converts raw octal special bits into Go FileMode flags.
func ModeToFlags(mode os.FileMode) os.FileMode {
	raw := mode & (rawSetuid | rawSetgid | rawSticky)
	if raw == 0 {
		return mode
	}

	var flags os.FileMode
	if raw&rawSetuid != 0 {
		flags |= os.ModeSetuid
	}
	if raw&rawSetgid != 0 {
		flags |= os.ModeSetgid
	}
	if raw&rawSticky != 0 {
		flags |= os.ModeSticky
	}
	return (mode &^ (rawSetuid | rawSetgid | rawSticky)) | flags
}

// NormalizeMode validates a mode and converts raw octal special bits into Go
// FileMode flags. It accepts both 0o4755 and 0o755|os.ModeSetuid forms.
func NormalizeMode(mode os.FileMode) os.FileMode {
	if invalid := mode &^ modeMax &^ (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); invalid != 0 {
		logger.Fatal("WithMode value %#o has bits %#o outside 0o7777: setuid/setgid/sticky (0o4000/0o2000/0o1000) plus the nine permission bits are the only supported mode bits", mode, invalid)
	}
	return ModeToFlags(mode)
}

// ModeToWire renders a mode in the canonical plan-wire octal format.
func ModeToWire(mode os.FileMode) string {
	wire := mode & os.ModePerm
	if mode&os.ModeSetuid != 0 {
		wire |= rawSetuid
	}
	if mode&os.ModeSetgid != 0 {
		wire |= rawSetgid
	}
	if mode&os.ModeSticky != 0 {
		wire |= rawSticky
	}
	return fmt.Sprintf("%#o", wire)
}
