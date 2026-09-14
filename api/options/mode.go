package options

import (
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/logger"
)

// Raw special permission bits above the 9 permission bits (0o777): the
// setuid/setgid/sticky bits as they appear in octal mode literals such as
// 0o4755. Go's os.FileMode does not use these bit positions for the special
// bits — it carries them as the ModeSetuid/ModeSetgid/ModeSticky flag bits
// instead — so raw values must be converted before chmod/serialization.
const (
	rawSetuid os.FileMode = 0o4000
	rawSetgid os.FileMode = 0o2000
	rawSticky os.FileMode = 0o1000
)

// modeMax is the largest mode value gonf accepts: the three special bits plus
// the nine permission bits. Anything above cannot be represented by chmod or
// by the plan wire format and is a programmer error.
const modeMax os.FileMode = 0o7777

// ModeToFlags converts the raw octal special bits 0o4000/0o2000/0o1000 into
// the os.ModeSetuid/ModeSetgid/ModeSticky flag bits, clearing the raw bits
// from the permission part. Go only honors the special bits through those
// flags, so both os.Chmod and the plan wire format need the flag form.
// Flag-style values pass through unchanged: the raw bit positions (9-11) are
// not used by Go's FileMode flags, so mode&0o7000 == 0 means already flag
// form (or plain permissions). Used by plan's parseMode and by normalizeMode.
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

// normalizeMode converts a raw octal mode literal (0o4755-style) into Go
// FileMode flag form (0o755|os.ModeSetuid) so the special bits survive
// os.Chmod and plan serialization. Flag-style values (the special bits
// already carried as ModeSetuid/ModeSetgid/ModeSticky) pass through
// unchanged, so the function is idempotent. A mode with bits above 0o7777
// cannot be represented on the wire or by chmod and is registration-time DSL
// misuse: it aborts via logger.Fatal like other unsupported option values.
func normalizeMode(mode os.FileMode) os.FileMode {
	if invalid := mode &^ modeMax &^ (os.ModeSetuid | os.ModeSetgid | os.ModeSticky); invalid != 0 {
		logger.Fatal("WithMode value %#o has bits %#o outside 0o7777: setuid/setgid/sticky (0o4000/0o2000/0o1000) plus the nine permission bits are the only supported mode bits", mode, invalid)
	}
	return ModeToFlags(mode)
}

// ModeToWire renders mode in the canonical plan-wire format: an octal string
// with a leading zero, four digits when setuid/setgid/sticky are set (e.g.
// "04755") and otherwise the 9-permission-bit form (e.g. "0640", "0750").
// ModeSetuid/ModeSetgid/ModeSticky are OR-ed back into the low octal bits;
// any other FileMode flag bits (ModeDir and friends) are ignored, matching
// chmod's view of the mode. This is the single home of plan mode
// serialization: plan.FormatMode and every resource PlanDraft Mode field go
// through it, so a WithMode(0o755|os.ModeSetuid) lowers to "04755" on the
// wire instead of being masked away.
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
