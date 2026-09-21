package api

import (
	"path/filepath"
	"slices"

	"github.com/snonux/gonf/internal/logger"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/options"
)

// loginClassDir is where OpenBSD looks up per-class fragments. It is a
// variable only so tests can point fragments at a private temp directory;
// recipes always get the native location.
var loginClassDir = "/etc/login.conf.d"

// loginClassRequirement is the refusal text recorded into the plan's
// requirement block; the plan engine appends the destination GOOS.
const loginClassRequirement = "only OpenBSD reads per-class fragments from /etc/login.conf.d " +
	"(FreeBSD and NetBSD keep classes in /etc/login.conf, Linux has no login classes)"

// LoginClass installs one OpenBSD login class as its own fragment,
// /etc/login.conf.d/<class>, from src (root:wheel 0644 by default; opts are
// appended after those defaults). It returns the class's change handle for
// OnChange on the daemon that must restart to pick up new limits:
//
//	class := LoginClass("inetd", src)
//	Service("inetd", WithRestart, OnChange(flags, class, config))
//
// Why no database rebuild: OpenBSD's login_getclass(3) looks up
// /etc/login.conf.d/<class> before /etc/login.conf on every lookup, reading
// the fragment as text. cap_mkdb(1) compiles only the files it is given, so
// `cap_mkdb /etc/login.conf` would not include the fragment and would only
// activate unrelated hand edits in /etc/login.conf. Nothing is rebuilt.
//
// getcap(3) does prefer a compiled <file>.db over the text file, so a
// /etc/login.conf.d/<class>.db (only ever made by running cap_mkdb on this
// fragment by hand) would shadow the managed text. gonf owns the class, so
// that stale database is removed; its removal is part of the change handle
// because it changes the class the daemon sees.
//
// Everything is recorded inside a plan requirement block (schema 20): on a
// destination whose GOOS is not openbsd, apply and dry run both refuse the
// whole plan before anything is written or predicted.
//
// With IsAbsent in opts the class is removed instead (see NoLoginClass) and
// src is ignored. LoginClass creates no accounts, home or runtime
// directories and does not create /etc/login.conf.d; User and Dir remain
// separate resources, ordered with DependsOn(class) where needed.
func LoginClass(class, src string, opts ...options.FileOption) Resource {
	validateLoginClassName(class)
	intent := inspectLoginClassOptions(class, opts)
	if intent.absent {
		return loginClassRemoval(class, opts)
	}
	if !intent.hasContentOverride() && src == "" {
		logger.Fatal("LoginClass %q: no content; pass a source file or WithContent", class)
	}
	source := ""
	if src != "" {
		source = Expand(src)
	}
	content, known := intent.installedContent(source)
	validateLoginClassContent(class, content, known)

	fileOpts := []options.FileOption{
		options.WithMode(0o644),
		options.WithOwner("root"),
		options.WithGroup("wheel"),
	}
	if source != "" {
		fileOpts = append([]options.FileOption{options.WithSource(source)}, fileOpts...)
	}
	fileOpts = append(fileOpts, slices.Clone(opts)...)

	var handle resource.Multi
	requireGOOS("openbsd", loginClassID(class), loginClassRequirement, func() {
		handle = resource.Multi{
			loginClassStaleDB(class),
			file.Present(filepath.Join(loginClassDir, class), fileOpts...),
		}
	})
	return handle
}

// NoLoginClass removes the OpenBSD fragment /etc/login.conf.d/<class> and any
// stale compiled <class>.db beside it, under the same OpenBSD requirement as
// LoginClass. The returned handle reports a change when either was removed.
func NoLoginClass(class string, opts ...options.FileOption) Resource {
	return LoginClass(class, "", append(slices.Clone(opts), options.IsAbsent)...)
}

// loginClassRemoval records the removal of a class fragment and its stale
// database. No source is needed or read; caller opts (e.g. DependsOn) apply
// to the fragment removal.
func loginClassRemoval(class string, opts []options.FileOption) Resource {
	var handle resource.Multi
	requireGOOS("openbsd", loginClassID(class), loginClassRequirement, func() {
		handle = resource.Multi{
			loginClassStaleDB(class),
			file.Present(filepath.Join(loginClassDir, class), opts...),
		}
	})
	return handle
}

// loginClassStaleDB removes /etc/login.conf.d/<class>.db, which getcap(3)
// would otherwise read instead of the managed text fragment.
func loginClassStaleDB(class string) resource.Resource {
	return file.Present(filepath.Join(loginClassDir, class+".db"), options.IsAbsent)
}

func loginClassID(class string) string { return "LoginClass[" + class + "]" }
