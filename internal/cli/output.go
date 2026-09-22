package cli

import (
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/internal/logger"
)

// eprintf writes a CLI message to stderr through the logger's redactor
// (logger.Redact: the controller's secret registry, installed by api), so an
// error that quotes a resolved secret — a failing command's output, an
// identity — never prints it. It is the only way the CLI writes to stderr.
func eprintf(format string, args ...any) {
	_, _ = fmt.Fprint(os.Stderr, logger.Redact(fmt.Sprintf(format, args...)))
}

// eprintErr prints a failed command's error as "prefix: err" (eprintf). A
// declaration error (internal/declerr: DSL misuse or a MustSecret failure
// inside a task body) also gets a second line naming the recipe location it
// was reported at, when known.
func eprintErr(prefix string, err error) {
	eprintf("%s: %v\n", prefix, err)
	if loc := declerr.Location(err); loc != "" {
		eprintf("%s: declared at %s\n", prefix, loc)
	}
}

// eprintln is eprintf with fmt.Sprintln formatting.
func eprintln(args ...any) {
	_, _ = fmt.Fprint(os.Stderr, logger.Redact(fmt.Sprintln(args...)))
}
