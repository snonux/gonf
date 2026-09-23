// Package internal holds gonf-wide constants that need no dependencies.
package internal

// Version is the gonf release version, reported by `gonf -version`.
const Version = "0.16.4"

// StrictPreviewVersion is the remote capability version required for
// `gonf apply -strict-preview`. It is separate from the plan wire version:
// strict-preview changes transport/apply safety behavior without changing a
// plan's encoding.
const StrictPreviewVersion = 1
