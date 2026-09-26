package gendesc

import (
	"bytes"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"strings"
)

// platform is one GOOS/GOARCH port and whether it supports cgo.
type platform struct {
	goos, goarch string
	cgo          bool
}

func (p platform) String() string { return p.goos + "/" + p.goarch }

// platforms are the ports of `go tool dist list -json` (Go 1.26) with their
// CgoSupported flag. Regenerate the list when a Go release adds or drops a
// port; TestPlatformsKnown fails on a port the toolchain no longer has.
var platforms = []platform{
	{"aix", "ppc64", true},
	{"android", "386", true},
	{"android", "amd64", true},
	{"android", "arm", true},
	{"android", "arm64", true},
	{"darwin", "amd64", true},
	{"darwin", "arm64", true},
	{"dragonfly", "amd64", true},
	{"freebsd", "386", true},
	{"freebsd", "amd64", true},
	{"freebsd", "arm", true},
	{"freebsd", "arm64", true},
	{"illumos", "amd64", true},
	{"ios", "amd64", true},
	{"ios", "arm64", true},
	{"js", "wasm", false},
	{"linux", "386", true},
	{"linux", "amd64", true},
	{"linux", "arm", true},
	{"linux", "arm64", true},
	{"linux", "loong64", true},
	{"linux", "mips", true},
	{"linux", "mips64", true},
	{"linux", "mips64le", true},
	{"linux", "mipsle", true},
	{"linux", "ppc64", false},
	{"linux", "ppc64le", true},
	{"linux", "riscv64", true},
	{"linux", "s390x", true},
	{"netbsd", "386", true},
	{"netbsd", "amd64", true},
	{"netbsd", "arm", true},
	{"netbsd", "arm64", true},
	{"openbsd", "386", true},
	{"openbsd", "amd64", true},
	{"openbsd", "arm", true},
	{"openbsd", "arm64", true},
	{"openbsd", "ppc64", false},
	{"openbsd", "riscv64", true},
	{"plan9", "386", false},
	{"plan9", "amd64", false},
	{"plan9", "arm", false},
	{"solaris", "amd64", true},
	{"wasip1", "wasm", false},
	{"windows", "386", true},
	{"windows", "amd64", true},
	{"windows", "arm64", true},
}

// target is one configuration a plain go build can select files for: a
// port with CGO_ENABLED=0, or, where the port supports it, =1.
type target struct {
	p   platform
	cgo bool
}

// targets are every port without cgo, each followed by the same port with
// cgo when it supports cgo.
var targets = func() []target {
	var ts []target
	for _, p := range platforms {
		ts = append(ts, target{p, false})
		if p.cgo {
			ts = append(ts, target{p, true})
		}
	}
	return ts
}()

// targetSet holds, per entry of targets, whether something builds there.
type targetSet []bool

// buildTargets returns the targets on which go build, without custom
// -tags, selects the file name with content src: its GOOS/GOARCH name
// suffix, its //go:build (or legacy +build) constraint and, when it imports
// "C", cgo all count. Release tags (go1.N) are the running toolchain's;
// no custom or tool tags (amd64.v3, goexperiment.*) are set, so a file
// needing one, or ignore, selects no target, and src is not parsed further.
func buildTargets(name string, src []byte) (targetSet, error) {
	set := make(targetSet, len(targets))
	for i, t := range targets {
		ctxt := build.Context{
			GOOS:        t.p.goos,
			GOARCH:      t.p.goarch,
			CgoEnabled:  t.cgo,
			Compiler:    "gc",
			ReleaseTags: build.Default.ReleaseTags,
			OpenFile: func(string) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(src)), nil
			},
		}
		ok, err := ctxt.MatchFile(".", name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		set[i] = ok
	}
	if set.none() {
		return set, nil
	}
	// MatchFile leaves the cgo rule to Import: a file importing "C" is
	// ignored when cgo is disabled.
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	for _, imp := range f.Imports {
		if imp.Path.Value == `"C"` {
			for i, t := range targets {
				set[i] = set[i] && t.cgo
			}
			break
		}
	}
	return set, nil
}

// none reports whether s holds no target.
func (s targetSet) none() bool {
	for _, ok := range s {
		if ok {
			return false
		}
	}
	return true
}

// all reports whether s holds every target.
func (s targetSet) all() bool {
	for _, ok := range s {
		if !ok {
			return false
		}
	}
	return true
}

// union adds the targets of o to s.
func (s targetSet) union(o targetSet) {
	for i, ok := range o {
		s[i] = s[i] || ok
	}
}

// minus returns the targets of s that are not in o.
func (s targetSet) minus(o targetSet) targetSet {
	d := make(targetSet, len(s))
	for i := range s {
		d[i] = s[i] && !o[i]
	}
	return d
}

// String names the targets of s briefly: "every platform" (optionally
// "with cgo" or "without cgo") when that is what s is, otherwise the ports,
// each qualified by "with cgo" or "without cgo" when s holds only that
// variant, the first few followed by how many more.
func (s targetSet) String() string {
	if s.all() {
		return "every platform"
	}
	onlyCgo, onlyNoCgo := true, true
	for i, t := range targets {
		if s[i] != t.cgo {
			onlyCgo = false
		}
		if s[i] == t.cgo {
			onlyNoCgo = false
		}
	}
	switch {
	case onlyCgo:
		return "every platform with cgo"
	case onlyNoCgo:
		return "every platform without cgo"
	}
	var labels []string
	for i := 0; i < len(targets); i++ {
		t := targets[i]
		noCgo, withCgo := s[i], false
		if t.p.cgo {
			i++
			withCgo = s[i]
		}
		switch {
		case noCgo && (withCgo || !t.p.cgo):
			labels = append(labels, t.p.String())
		case noCgo:
			labels = append(labels, t.p.String()+" without cgo")
		case withCgo:
			labels = append(labels, t.p.String()+" with cgo")
		}
	}
	const shown = 3
	if len(labels) > shown+1 {
		return fmt.Sprintf("%s and %d more", strings.Join(labels[:shown], ", "), len(labels)-shown)
	}
	return strings.Join(labels, ", ")
}
