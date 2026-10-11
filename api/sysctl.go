package api

import (
	"fmt"
	"slices"
	"strings"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/options"
)

// Sysctls owns ordered key=value settings in path and reconciles their live
// values on every apply. kv contains alternating keys and values. It composes
// one File and one Command per distinct key, returning all their dependency
// handles. Commands depend on the file and use sysctl -n plus ExpectStdout to
// skip matching values; they are not gated on file changes. opts configure the
// file (ownership, mode, name, dependencies, sensitivity); IsAbsent is refused.
// Values must be non-empty, single-line, contain no # or NUL, and have no
// surrounding whitespace.
// Keys use letters, digits, underscores and dots, starting with a letter or
// underscore. Exact repeated pairs are collapsed; conflicting values, invalid
// pairs and option misuse are declaration errors. Empty kv registers nothing.
// File refusal registers no live commands. WithSensitive also protects the
// commands. Host selection, privilege and which settings to manage belong to
// the recipe. The destination must provide sysctl supporting -n and key=value.
func Sysctls(path string, kv []string, opts ...options.FileOption) Resource {
	pairs, err := sysctlPairs(kv)
	if err != nil {
		declerr.Report(err)
		return resource.Multi(nil)
	}
	if len(pairs) == 0 {
		return resource.Multi(nil)
	}
	all, err := sysctlOptions(path, opts)
	if err != nil {
		declerr.Report(err)
		return resource.Multi(nil)
	}
	for _, p := range pairs {
		all = append(all, WithKeyedLine(p[0]+"=", p[0]+"="+p[1]))
	}
	before := len(resource.RegisteredIDs())
	conf := file.Present(path, all...)
	if len(resource.RegisteredIDs()) == before {
		return resource.Multi(nil)
	}
	_, registered, _ := resource.Registered(conf.ID())
	configured, _ := registered.(*file.File)
	result := resource.Multi{conf}
	for _, p := range pairs {
		commandOpts := []options.CommandOption{WithName("sysctl." + p[0]), DependsOn(conf), Unless("sysctl", List("-n", p[0]), ExpectStdout(p[1]))}
		if configured != nil && configured.Sensitive {
			commandOpts = append(commandOpts, WithSensitive)
		}
		result = append(result, cmd.Present("sysctl", List(p[0]+"="+p[1]), commandOpts...))
	}
	return result
}

func sysctlPairs(kv []string) ([][2]string, error) {
	pairs, err := ParseKV(kv)
	if err != nil {
		return nil, fmt.Errorf("Sysctls: %w", err)
	}
	seen := make(map[string]string, len(pairs))
	out := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		if !validSysctlKey(p[0]) {
			return nil, fmt.Errorf("Sysctls: invalid key %q", p[0])
		}
		if p[1] == "" || p[1] != strings.TrimSpace(p[1]) || strings.ContainsAny(p[1], "\r\n\x00#") {
			return nil, fmt.Errorf("Sysctls: value for %q must be non-empty, single-line, without # or NUL, and without surrounding whitespace", p[0])
		}
		if old, ok := seen[p[0]]; ok {
			if old != p[1] {
				return nil, fmt.Errorf("Sysctls: conflicting values for %q", p[0])
			}
			continue
		}
		seen[p[0]] = p[1]
		out = append(out, p)
	}
	return out, nil
}

func validSysctlKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && (r == '.' || r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return true
}

// sysctlOptions appends a check to the real File's options instead of applying
// caller options to a probe first: even a legacy custom option runs only once.
func sysctlOptions(path string, opts []options.FileOption) ([]options.FileOption, error) {
	if path == "" {
		return nil, fmt.Errorf("Sysctls: empty configuration path")
	}
	for _, o := range opts {
		if o == nil {
			return nil, fmt.Errorf("Sysctls: nil file option")
		}
	}
	check := options.Option(func(target any) {
		if f, ok := target.(*file.File); ok && f.Absent {
			f.ReportMisuse(fmt.Errorf("Sysctls: IsAbsent cannot manage persistent and live settings"))
		}
	})
	return append(slices.Clone(opts), options.ToFileOptions(check)...), nil
}
