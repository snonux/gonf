package gendesc

import (
	"bytes"
	"fmt"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"os"
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

// archLevels are the architecture-level tags go build sets by default for
// a GOARCH (GO386=sse2, GOAMD64=v1, GOARM=7, GOARM64=v8.0,
// GOMIPS(64)=hardfloat, GOPPC64=power8, GORISCV64=rva20u64, wasm's
// always-on features), cumulative as in internal/buildcfg.
var archLevels = map[string][]string{
	"386":      {"386.sse2"},
	"amd64":    {"amd64.v1"},
	"arm":      {"arm.5", "arm.6", "arm.7"},
	"arm64":    {"arm64.v8.0"},
	"mips":     {"mips.hardfloat"},
	"mipsle":   {"mipsle.hardfloat"},
	"mips64":   {"mips64.hardfloat"},
	"mips64le": {"mips64le.hardfloat"},
	"ppc64":    {"ppc64.power8"},
	"ppc64le":  {"ppc64le.power8"},
	"riscv64":  {"riscv64.rva20u64"},
	"wasm":     {"wasm.satconv", "wasm.signext"},
}

// experimentTags returns the goexperiment.* tool tags a go build for
// goos/goarch sets. host are the running toolchain's tool tags, for the host
// platform and with goexp (the GOEXPERIMENT environment) applied. Most
// experiments do not depend on the platform and are taken from host as
// they are; the ones that do are redone as internal/buildcfg's
// ParseGOEXPERIMENT (Go 1.26) does: regabiwrappers and regabiargs are on
// where the register ABI is supported (always on amd64, arm64, loong64,
// ppc64, ppc64le and riscv64; by default, overridable, on s390x), dwarf5
// is on except on darwin, ios and aix, and goexp's X or noX (and the regabi
// alias, and none) wins over those defaults.
func experimentTags(host []string, goexp, goos, goarch string) []string {
	const regabiWrappers, regabiArgs, dwarf5 = "regabiwrappers", "regabiargs", "dwarf5"
	var tags []string
	for _, t := range host {
		name, ok := strings.CutPrefix(t, "goexperiment.")
		if ok && name != regabiWrappers && name != regabiArgs && name != dwarf5 {
			tags = append(tags, t)
		}
	}
	var alwaysOn, supported bool
	switch goarch {
	case "amd64", "arm64", "loong64", "ppc64", "ppc64le", "riscv64":
		alwaysOn, supported = true, true
	case "s390x":
		supported = true
	}
	on := map[string]bool{
		regabiWrappers: supported,
		regabiArgs:     supported,
		dwarf5:         goos != "darwin" && goos != "ios" && goos != "aix",
	}
	for _, f := range strings.Split(goexp, ",") {
		if f == "none" {
			clear(on)
			continue
		}
		name, off := strings.CutPrefix(f, "no")
		switch name {
		case "regabi":
			on[regabiWrappers], on[regabiArgs] = !off, !off
		case regabiWrappers, regabiArgs, dwarf5:
			on[name] = !off
		}
	}
	if alwaysOn || !supported {
		on[regabiWrappers], on[regabiArgs] = alwaysOn, alwaysOn
	}
	for _, name := range []string{regabiWrappers, regabiArgs, dwarf5} {
		if on[name] {
			tags = append(tags, "goexperiment."+name)
		}
	}
	return tags
}

// target is one configuration a plain go build can select files for: a
// port with CGO_ENABLED=0, or, where the port supports it, =1.
type target struct {
	p   platform
	cgo bool
	// toolTags are the tool tags a default go build sets there: the
	// port's architecture levels and its experiments.
	toolTags []string
}

// targets are every port without cgo, each followed by the same port with
// cgo when it supports cgo.
var targets = func() []target {
	var ts []target
	for _, p := range platforms {
		tags := append(append([]string(nil), archLevels[p.goarch]...),
			experimentTags(build.Default.ToolTags, os.Getenv("GOEXPERIMENT"), p.goos, p.goarch)...)
		ts = append(ts, target{p, false, tags})
		if p.cgo {
			ts = append(ts, target{p, true, tags})
		}
	}
	return ts
}()

// targetSet holds, per entry of targets, whether something builds there.
type targetSet []bool

// buildTargets returns the targets on which go build, without custom
// -tags, selects the file name with content src: its GOOS/GOARCH name
// suffix, its //go:build (or legacy +build) constraint and, when it imports
// "C", cgo all count. Release tags (go1.N) are the running toolchain's,
// and each port has its default architecture level (see archLevels) and
// experiments (see experimentTags). A file needing a custom tag
// (ignore, integration) or a non-default level (amd64.v3) selects no
// target, and src is not parsed further.
func buildTargets(name string, src []byte) (targetSet, error) {
	set := make(targetSet, len(targets))
	for i, t := range targets {
		ctxt := build.Context{
			GOOS:        t.p.goos,
			GOARCH:      t.p.goarch,
			CgoEnabled:  t.cgo,
			Compiler:    "gc",
			ReleaseTags: build.Default.ReleaseTags,
			ToolTags:    t.toolTags,
			OpenFile: func(string) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(src)), nil
			},
		}
		ok, err := ctxt.MatchFile(".", name)
		if err != nil {
			return nil, err // it names the file
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
