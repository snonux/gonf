package systemd

import "github.com/snonux/gonf/resource"

// resource.Register takes a DaemonReloadResource as a resource.Applier. The
// assertion pins that contract at the declaration, so a renamed or re-signed
// Apply is reported here rather than at the Register call.
//
// It lives in its own file only because task k62 was rewriting
// daemon_reload.go at the same time; it belongs in that file's var block of
// opt.* assertions, like every other resource's.
var _ resource.Applier = (*DaemonReloadResource)(nil)
