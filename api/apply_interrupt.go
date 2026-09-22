package api

import (
	"context"
	"errors"
	"fmt"
	"io"

	gexec "github.com/snonux/gonf/internal/exec"
)

// validatorWaitNotice is the line printed when an interrupt arrives while a
// File or ConfigSet validator runs; %v is the command timeout bounding it.
const validatorWaitNotice = "gonf: interrupt received; waiting for the running validator to finish " +
	"(validators are bounded by the command timeout, %v)\n"

// noteValidatorWait arranges that, once ctx is canceled (SIGINT/SIGTERM),
// the operator is told on w when a validator (running reports one) is still
// executing. Validators are deliberately not bound to the apply's signal
// context, only to the command timeout, so without this an interrupted apply
// would seem to hang silently for up to that timeout. A deadline is not an
// interrupt and prints nothing. The returned stop disarms the notice; call
// it when the apply returns.
func noteValidatorWait(ctx context.Context, w io.Writer, running func() bool) (stop func() bool) {
	return context.AfterFunc(ctx, func() {
		if errors.Is(ctx.Err(), context.Canceled) && running() {
			_, _ = fmt.Fprintf(w, validatorWaitNotice, gexec.DefaultTimeout())
		}
	})
}
