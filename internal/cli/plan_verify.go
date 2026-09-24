package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/plan/seal"
)

// This file is `gonf plan-verify`, the verify-only unwrap helper of task
// 8g2 (docs/design/plan-signing.md "Emergency path"; the design sketched it as
// `gonf plan -verify-only`, and it landed as its own subcommand so it does
// not overload `gonf plan`'s task arguments). A signed plan is no longer a
// bare age stream, so the emergency path `age -d -i key plan.age | gonf
// apply -` needs one step first:
//
//	gonf plan-verify -trusted-signers signers signed.age > plan.age
//	age -d -i key.txt plan.age | gonf apply -
//
// It runs exactly the checks `gonf apply` runs on a signed plan (the
// trusted-signers file hardening, the signature, then the freshness
// window, verifySignedPlan) and, only when all pass, writes the untouched
// plan.age bytes to stdout. It decrypts nothing, needs no identity, and
// never writes anything for an input it refused.

// planVerifyUsage is plan-verify's usage line, shared by its own refusal
// and printUsage.
const planVerifyUsage = "gonf plan-verify [-trusted-signers file]... [-max-signed-age 24h] <signed-plan|->"

// cliPlanVerify is `gonf plan-verify`: verify a signed plan (file, or "-"
// for stdin) and write its bare plan.age to stdout. Exit 2 for a usage
// error, 1 for any refusal (nothing written to stdout), 0 on success.
func cliPlanVerify(args []string) int {
	fs := flag.NewFlagSet("plan-verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	signing := registerSigningFlags(fs, false)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		eprintln("usage: " + planVerifyUsage)
		return 2
	}
	if msg := signing.conflict(); msg != "" {
		eprintln("plan-verify: " + msg)
		return 2
	}
	v, src, err := verifyPlanVerifyInput(fs.Arg(0), signing)
	if err != nil {
		eprintf("plan-verify: %v; nothing written\n", err)
		return 1
	}
	if _, err := os.Stdout.Write(v.Sealed); err != nil {
		eprintf("plan-verify: write stdout: %v\n", err)
		return 1
	}
	eprintf("plan-verify: %s: %s; wrote its plan.age to stdout (%d bytes, still sealed, nothing decrypted)\n",
		src, verifiedNote(v, signing.maxAge), len(v.Sealed))
	return 0
}

// verifyPlanVerifyInput reads arg (a plan file through
// plan.ReadPrivateFilePath, the same no-symlink, regular-file read gonf
// apply uses, or stdin for "-", capped at maxSignedEnvelopeBytes), refuses
// an input that is not a signed plan, and verifies it (verifySignedPlan).
// src is how messages name the input ("stdin" for "-").
func verifyPlanVerifyInput(arg string, signing *signingFlags) (v seal.Verified, src string, err error) {
	var env []byte
	src = arg
	if arg == "-" {
		src = "stdin"
		env, err = readSignedStream(os.Stdin, maxSignedEnvelopeBytes)
	} else {
		env, err = plan.ReadPrivateFilePath(arg)
	}
	if err != nil {
		return seal.Verified{}, src, fmt.Errorf("read %s: %w", src, err)
	}
	if classifyPlanInput(env) != signedInput {
		return seal.Verified{}, src, fmt.Errorf("%s is not a signed plan (no %s envelope)", src, seal.SignedPlanMagic)
	}
	if v, err = verifySignedPlan(env, signing); err != nil {
		return seal.Verified{}, src, fmt.Errorf("%s: signed plan refused: %w", src, err)
	}
	return v, src, nil
}
