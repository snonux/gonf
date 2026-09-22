package options

import (
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/declerr"
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
// FileMode flags. It accepts both 0o4755 and 0o755|os.ModeSetuid forms. A mode
// with bits outside 0o7777 (a file-type bit, say) is recipe misuse: it is
// reported as a declaration error (internal/declerr) and those bits are
// dropped from the result. WithMode and WithFileMode report the same misuse
// to the resource they configure instead.
func NormalizeMode(mode os.FileMode) os.FileMode {
	normalized, err := normalizeMode(mode)
	if err != nil {
		declerr.Report(err)
		return ModeToFlags(mode & (modeMax | os.ModeSetuid | os.ModeSetgid | os.ModeSticky))
	}
	return normalized
}

// normalizeMode is NormalizeMode's checked core: the flags form of mode, or an
// error naming the bits outside 0o7777.
func normalizeMode(mode os.FileMode) (os.FileMode, error) {
	if invalid := mode &^ modeMax &^ (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); invalid != 0 {
		return 0, fmt.Errorf("WithMode value %#o has bits %#o outside 0o7777: setuid/setgid/sticky (0o4000/0o2000/0o1000) plus the nine permission bits are the only supported mode bits", mode, invalid)
	}
	return ModeToFlags(mode), nil
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
