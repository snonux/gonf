package api

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/snonux/gonf/internal/declerr"
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
//
// Misuse — an invalid class name, line-edit or unsupported options, no content,
// or content that does not define the class — is reported as a declaration
// error (internal/declerr, which fails the record) and nothing is declared:
// the returned handle is then an empty Multi.
func LoginClass(class, src string, opts ...options.FileOption) Resource {
	intent, err := inspectLoginClassOptions(class, opts)
	if err != nil {
		declerr.Report(err)
		return resource.Multi(nil)
	}
	if intent.absent {
		return loginClassRemoval(class, opts)
	}
	source, err := checkLoginClassContent(class, src, intent)
	if err != nil {
		declerr.Report(err)
		return resource.Multi(nil)
	}

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

// checkLoginClassContent returns the expanded default source of a present
// class, or the error refusing it: no content at all, or content known on the
// controller that does not define the class (validateLoginClassContent).
func checkLoginClassContent(class, src string, intent *loginClassProbe) (string, error) {
	if !intent.hasContentOverride() && src == "" {
		return "", fmt.Errorf("LoginClass %q: no content; pass a source file or WithContent", class)
	}
	source := ""
	if src != "" {
		source = Expand(src)
	}
	content, known := intent.installedContent(source)
	return source, validateLoginClassContent(class, content, known)
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

// loginClassID names a LoginClass composition in its requirement block and
// refusal text. LoginClass is not a registered resource (it composes File
// resources), but it uses the shared resource.FormatID spelling so every
// Type[Name] identifier gonf prints has one definition.
func loginClassID(class string) string { return resource.FormatID("LoginClass", class) }
