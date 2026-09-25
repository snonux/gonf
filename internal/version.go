// Package internal holds gonf-wide constants that need no dependencies.
package internal

// Version is the gonf release version, reported by `gonf -version`.
const Version = "0.22.0"

// StrictPreviewVersion is the remote capability version required for
// `gonf apply -strict-preview`. It is separate from the plan wire version:
// strict-preview changes transport/apply safety behavior without changing a
// plan's encoding.
const StrictPreviewVersion = 1

// SealedVersion is the sealed-plan capability version this binary can
// decrypt and apply (`gonf apply -identity ... plan.age`), printed by
// `gonf -sealed-version` next to -plan-version/-strict-preview-version.
// Separate from both: it names what the age(GONF-PUSH/1) container this
// binary understands looks like (docs/design/plan-encryption.md, "Schema,
// versioning and remote skew"), not the inner plan schema or the
// strict-preview transport behavior, either of which can change without
// this one moving.
const SealedVersion = 1

// SignedVersion is the signed-plan envelope version this binary can verify
// (`gonf apply -trusted-signers ...`, `gonf plan-verify`; task 8g2), printed
// by `gonf -signed-version` next to -sealed-version. It is the N of the
// GONF-SIGNED-PLAN/N magic plan/seal verifies (docs/design/plan-signing.md,
// "Schema, versioning and remote skew"), independent of SealedVersion: the
// envelope wraps the sealed artifact without changing it. A binary that
// could verify several versions would print each, one per line.
const SignedVersion = 1
