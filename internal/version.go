// Package internal holds gonf-wide constants that need no dependencies.
package internal

// Version is the gonf release version, reported by `gonf -version`.
const Version = "0.16.6"

// StrictPreviewVersion is the remote capability version required for
// `gonf apply -strict-preview`. It is separate from the plan wire version:
// strict-preview changes transport/apply safety behavior without changing a
// plan's encoding.
const StrictPreviewVersion = 1

// SealedVersion is the sealed-plan capability version this binary can
// decrypt and apply (`gonf apply -identity ... plan.age`), printed by
// `gonf -sealed-version` next to -plan-version/-strict-preview-version.
// Separate from both: it names what the age(GONF-PUSH/1) container this
// binary understands looks like (docs/plan-encryption.md, "Schema,
// versioning and remote skew"), not the inner plan schema or the
// strict-preview transport behavior, either of which can change without
// this one moving.
const SealedVersion = 1
