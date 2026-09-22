package configset

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// defaultMemberMode matches the File resource's default mode, so a member
// without WithMode is published exactly like a File without WithMode.
const defaultMemberMode os.FileMode = 0o640

// keyPattern restricts set names and member keys to characters that are safe
// inside resource IDs, staging directory names and placeholder tokens.
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// spec is the complete, validated description of a config set. The recipe
// path (Present) and the plan path (the config_set handler) both build one and
// run the same validate and apply code, so a crafted or stale plan line gets
// exactly the checks a recipe gets.
type spec struct {
	name       string
	members    []memberSpec
	validators []resource.PlanArgv
	chroot     string
	stagingDir string // explicit WithStagingDir, "" for the default
}

// memberSpec is one member file. owner and group are only non-empty when
// explicitly configured; unset they default to the applying user, as for File.
type memberSpec struct {
	key     string
	path    string
	content []byte
	mode    os.FileMode
	owner   string
	group   string
}

// setID and memberID are the report/dependency identities of a set and of
// one member handle.
func setID(name string) string           { return fmt.Sprintf("ConfigSet[%s]", name) }
func memberID(name, key string) string   { return fmt.Sprintf("ConfigSetMember[%s/%s]", name, key) }
func memberName(name, key string) string { return name + "/" + key }

func checkKey(key string) error {
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("invalid name %q (want letters, digits, '.', '_' or '-', not starting with a punctuation character)", key)
	}
	return nil
}

// validate checks everything that can be checked without touching the
// destination: names, absolute clean paths, duplicates, placement under the
// chroot and staging directory, validator binaries and placeholder
// references. It runs at record time and again before every apply.
func (s *spec) validate() error {
	if err := checkKey(s.name); err != nil {
		return fmt.Errorf("config set: %w", err)
	}
	if len(s.members) == 0 {
		return fmt.Errorf("config set %s: needs at least one ConfigFile member", s.name)
	}
	if len(s.validators) == 0 {
		return fmt.Errorf("config set %s: needs at least one WithSetValidation validator (use File for unvalidated files)", s.name)
	}
	if err := s.validateMembers(); err != nil {
		return err
	}
	if err := s.validatePlacement(); err != nil {
		return err
	}
	return s.validateReferences()
}

// validateMembers checks member keys and paths for shape and uniqueness.
func (s *spec) validateMembers() error {
	keys := map[string]bool{}
	paths := map[string]string{}
	for _, m := range s.members {
		if err := checkKey(m.key); err != nil {
			return fmt.Errorf("config set %s: member: %w", s.name, err)
		}
		if keys[m.key] {
			return fmt.Errorf("config set %s: duplicate member key %q", s.name, m.key)
		}
		keys[m.key] = true
		if err := checkAbsClean(m.path); err != nil {
			return fmt.Errorf("config set %s: member %s: %w", s.name, m.key, err)
		}
		if strings.HasSuffix(m.path, ".tmpl") {
			return fmt.Errorf("config set %s: member %s: a .tmpl path is not rendered in a config set; name the live file", s.name, m.key)
		}
		if other, dup := paths[m.path]; dup {
			return fmt.Errorf("config set %s: members %s and %s share path %s", s.name, other, m.key, m.path)
		}
		paths[m.path] = m.key
	}
	return nil
}

// validatePlacement checks the chroot and the staging directory: the staging
// directory must be an ancestor of every member (relative layout is mirrored
// below it) and, with a chroot, inside the chroot so staged candidates are
// visible to chroot-aware validators.
func (s *spec) validatePlacement() error {
	if s.chroot != "" {
		if err := checkAbsClean(s.chroot); err != nil {
			return fmt.Errorf("config set %s: chroot: %w", s.name, err)
		}
		for _, m := range s.members {
			if !within(s.chroot, m.path) {
				return fmt.Errorf("config set %s: member %s (%s) is outside chroot %s", s.name, m.key, m.path, s.chroot)
			}
		}
	}
	staging := s.stagingParent()
	if err := checkAbsClean(staging); err != nil {
		return fmt.Errorf("config set %s: staging directory: %w", s.name, err)
	}
	for _, m := range s.members {
		if !within(staging, filepath.Dir(m.path)) {
			return fmt.Errorf("config set %s: staging directory %s is not an ancestor of member %s (%s)", s.name, staging, m.key, m.path)
		}
	}
	if s.chroot != "" && !within(s.chroot, staging) {
		return fmt.Errorf("config set %s: staging directory %s is outside chroot %s", s.name, staging, s.chroot)
	}
	return nil
}

// validateReferences checks validator binaries and every placeholder in the
// member contents and validator arguments.
func (s *spec) validateReferences() error {
	keys := make(map[string]bool, len(s.members))
	for _, m := range s.members {
		keys[m.key] = true
	}
	check := func(where string, content []byte) error {
		refs, err := references(content)
		if err != nil {
			return fmt.Errorf("config set %s: %s: %w", s.name, where, err)
		}
		for _, ref := range refs {
			if !keys[ref.key] {
				return fmt.Errorf("config set %s: %s references unknown member %q", s.name, where, ref.key)
			}
			if ref.chroot && s.chroot == "" {
				return fmt.Errorf("config set %s: %s uses MemberChrootPath(%q) without WithChroot", s.name, where, ref.key)
			}
		}
		return nil
	}
	for _, m := range s.members {
		if err := check("member "+m.key, m.content); err != nil {
			return err
		}
	}
	for i, v := range s.validators {
		if v.Bin == "" || strings.Contains(v.Bin, tokenMarker) {
			return fmt.Errorf("config set %s: validator %d needs a literal binary", s.name, i+1)
		}
		for _, arg := range v.Args {
			if err := check(fmt.Sprintf("validator %d (%s)", i+1, v.Bin), []byte(arg)); err != nil {
				return err
			}
		}
	}
	return nil
}

// stagingParent is the directory the private staging directory is created
// in: WithStagingDir, or the deepest directory containing every member.
func (s *spec) stagingParent() string {
	if s.stagingDir != "" {
		return s.stagingDir
	}
	common := filepath.Dir(s.members[0].path)
	for _, m := range s.members[1:] {
		for !within(common, filepath.Dir(m.path)) {
			common = filepath.Dir(common)
		}
	}
	return common
}

// memberDirs returns the distinct parent directories of all members; these
// are the directories whose locks serialize overlapping publications.
func (s *spec) memberDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	for _, m := range s.members {
		dir := filepath.Dir(m.path)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

// targets builds a file.Target per member (same order as s.members). It does
// not look up owners or groups: a dry-run must succeed while an account the
// recipe creates earlier in the same run does not exist yet. The real apply
// resolves them separately (resolveOwnership) before its first write.
func (s *spec) targets() ([]*file.Target, error) {
	targets := make([]*file.Target, len(s.members))
	for i, m := range s.members {
		opts := []opt.FileOption{opt.WithMode(m.mode)}
		if m.owner != "" {
			opts = append(opts, opt.WithOwner(m.owner))
		}
		if m.group != "" {
			opts = append(opts, opt.WithGroup(m.group))
		}
		t, err := file.NewTarget(m.path, opts...)
		if err != nil {
			return nil, fmt.Errorf("config set %s: member %s: %w", s.name, m.key, err)
		}
		targets[i] = t
	}
	return targets, nil
}

// resolveOwnership resolves every member's owner and group (targets in the
// same order as s.members), so an unknown account fails the whole set before
// anything is staged or published instead of after some members were already
// replaced. Only the real apply calls it; a dry-run skips it, like a File,
// which resolves its owner only when it writes.
func (s *spec) resolveOwnership(targets []*file.Target) error {
	for i, t := range targets {
		if err := t.ResolveOwnership(); err != nil {
			return fmt.Errorf("config set %s: member %s: %w", s.name, s.members[i].key, err)
		}
	}
	return nil
}

// checkAbsClean requires an absolute path already in canonical form: no
// ".", "..", duplicate or trailing separators. A non-canonical path would make
// the ancestor checks above compare different spellings of the same place.
func checkAbsClean(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q is not an absolute path", path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%q is not a clean path (want %q)", path, filepath.Clean(path))
	}
	return nil
}

// within reports whether path is dir itself or lies below it. Both must be
// clean absolute paths.
func within(dir, path string) bool {
	if dir == path {
		return true
	}
	if dir == string(filepath.Separator) {
		return strings.HasPrefix(path, dir)
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}
