// Package configset implements ConfigSet: several configuration files that are
// rendered once, staged together in a private directory, validated as one
// complete candidate set, and only then published, with a hard-link based
// rollback when a publication fails part-way. Each member also gets its own
// report-only handle resource so OnChange can react to one member (e.g.
// newaliases for the aliases table) instead of to the whole set.
//
// See docs/config-set.md for the exact validation, serialization and
// partial-publication guarantees.
package configset

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/embed"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// ConfigSet collects a set's options at record time. It embeds
// embed.DependsOn for the DependsOn option and embed.Sensitivity for
// WithSensitive; removal is not supported, so it deliberately does not
// embed embed.Absence.
type ConfigSet struct {
	embed.DependsOn
	embed.Sensitivity
	embed.Misuse
	spec    spec
	members []*member
}

// member collects one ConfigFile's file options. sensitive is WithSensitive
// given to the member: one op carries every member, so it marks the whole
// set (build). Its embed.Misuse collects a misused member option, which
// AddMember hands to the set.
type member struct {
	embed.Misuse
	key, path    string
	content      []byte
	contentSet   bool
	source       string
	mode         os.FileMode
	owner, group string
	sensitive    bool
}

var (
	_ opt.Dependable     = (*ConfigSet)(nil)
	_ opt.MemberAddable  = (*ConfigSet)(nil)
	_ opt.SetValidatable = (*ConfigSet)(nil)
	_ opt.Chrootable     = (*ConfigSet)(nil)
	_ opt.StagingDirable = (*ConfigSet)(nil)
	_ opt.Contented      = (*member)(nil)
	_ opt.Sourced        = (*member)(nil)
	_ opt.Moded          = (*member)(nil)
	_ opt.Owner          = (*member)(nil)
	_ opt.Grouped        = (*member)(nil)
	_ opt.Sensitivable   = (*ConfigSet)(nil)
	_ opt.MisuseReporter = (*ConfigSet)(nil)
	_ opt.MisuseReporter = (*member)(nil)
	_ opt.Sensitivable   = (*member)(nil)
)

// AddMember implements opt.MemberAddable (the ConfigFile option). Only
// content, source, mode and ownership file options are supported; any other
// file option fails the set's build through the options package's capability
// check: the member's misuse is handed to the set (ReportMisuse).
func (c *ConfigSet) AddMember(key, path string, opts []opt.FileOption) {
	m := &member{key: key, path: path, mode: defaultMemberMode}
	for _, o := range opts {
		o.Apply(m)
	}
	if err := m.MisuseErr(); err != nil {
		c.ReportMisuse(err)
	}
	c.members = append(c.members, m)
}

// AddSetValidator implements opt.SetValidatable. args is copied so a caller
// reusing its slice cannot change the recorded validator.
func (c *ConfigSet) AddSetValidator(bin string, args []string) {
	c.spec.validators = append(c.spec.validators, resource.PlanArgv{Bin: bin, Args: slices.Clone(args)})
}

// SetChroot implements opt.Chrootable.
func (c *ConfigSet) SetChroot(root string) { c.spec.chroot = root }

// SetStagingDir implements opt.StagingDirable.
func (c *ConfigSet) SetStagingDir(dir string) { c.spec.stagingDir = dir }

// SetContent implements opt.Contented for a member.
func (m *member) SetContent(content string) {
	m.content, m.source, m.contentSet = []byte(content), "", true
}

// SetSource implements opt.Sourced for a member. The controller-local source
// is read once when the set is built (record time), never on the destination.
func (m *member) SetSource(source string) { m.source, m.content, m.contentSet = source, nil, true }

// SetMode implements opt.Moded for a member.
func (m *member) SetMode(mode os.FileMode) { m.mode = mode }

// SetOwner implements opt.Owner for a member.
func (m *member) SetOwner(owner string) { m.owner = owner }

// SetGroup implements opt.Grouped for a member.
func (m *member) SetGroup(group string) { m.group = group }

// SetSensitive implements opt.Sensitivable for a member (WithSensitive on a
// ConfigFile); build makes the whole set sensitive.
func (m *member) SetSensitive() { m.sensitive = true }

// build applies opts and turns the collected members into a validated spec,
// reading WithSource members from the controller.
func build(name string, opts []opt.ConfigSetOption) (*ConfigSet, error) {
	c := &ConfigSet{spec: spec{name: name}}
	for _, o := range opts {
		o.Apply(c)
	}
	if err := c.MisuseErr(); err != nil {
		return nil, err
	}
	// WithSensitive on the set or on any member marks the set: its one op
	// carries every member, and a failing validator sees them all.
	c.spec.sensitive = c.Sensitive
	for _, m := range c.members {
		ms, err := m.resolve()
		if err != nil {
			return nil, fmt.Errorf("config set %s: member %s: %w", name, m.key, err)
		}
		c.spec.members = append(c.spec.members, ms)
		c.spec.sensitive = c.spec.sensitive || m.sensitive
	}
	if err := c.spec.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// resolve returns the member's spec, reading a WithSource file once with the
// File resource's source rules (non-regular sources are refused, a FIFO never
// blocks the read). A ".tmpl" source is refused here and a ".tmpl" path by
// the spec validation: a File renders such templates, but a member's bytes
// are published as recorded, so accepting one would publish raw template
// text. Render the content in the recipe instead.
func (m *member) resolve() (memberSpec, error) {
	ms := memberSpec{key: m.key, path: m.path, mode: m.mode, owner: m.owner, group: m.group}
	switch {
	case strings.HasSuffix(m.source, ".tmpl"):
		return ms, fmt.Errorf("a .tmpl source is not rendered in a config set; render the content in the recipe and pass WithContent")
	case !m.contentSet:
		return ms, fmt.Errorf("needs WithContent or WithSource")
	case m.source != "":
		data, err := file.ReadSource(m.source)
		if err != nil {
			return ms, fmt.Errorf("read source: %w", err)
		}
		ms.content = data
	default:
		ms.content = slices.Clone(m.content)
	}
	return ms, nil
}

// Handle is what api.ConfigSet returns. The embedded Resource is the whole
// set (usable in DependsOn and OnChange: it changes when any member was
// published); Member returns one member's handle.
type Handle struct {
	resource.Resource
	name    string
	members map[string]resource.Resource
	keys    []string
}

// Member returns the handle of member key. It reports a change only when
// that member's live file was published. An unknown key is recipe misuse: it
// is reported as a declaration error (resource.Refuse, which fails the
// record) and an unregistered handle is returned.
func (h Handle) Member(key string) resource.Resource {
	r, ok := h.members[key]
	if !ok {
		return resource.Refuse("ConfigSetMember", memberName(h.name, key),
			fmt.Errorf("config set %s has no member %q (members: %v)", h.name, key, h.keys))
	}
	return r
}

// Members returns the handles of keys, in order, ready for OnChange or
// DependsOn. With no keys it returns every member handle.
func (h Handle) Members(keys ...string) []resource.Dependency {
	if len(keys) == 0 {
		keys = h.keys
	}
	out := make([]resource.Dependency, 0, len(keys))
	for _, key := range keys {
		out = append(out, h.Member(key))
	}
	return out
}

// Present registers the config set and one handle resource per member, and
// records their plan drafts. A misconfigured set is reported as a declaration
// error (resource.Refuse), which fails the record. The set's
// applier and its member appliers (the legacy resource.Apply path) share one
// outcome store of their own; the plan path uses the plan handlers' store
// instead (see newHandlers).
func Present(name string, opts ...opt.ConfigSetOption) Handle {
	c, err := build(name, opts)
	if err != nil {
		// A refused set has no member handles: Member reports every key.
		return Handle{Resource: resource.Refuse("ConfigSet", name, err), name: name}
	}
	sp := c.spec
	sp.sys, sp.outcomes = newSystem(), newOutcomeStore()
	set := resource.Register("ConfigSet", name, resource.ApplierFunc(sp.apply), c.DependsOn.IDs...)
	resource.RecordPlanDraft(sp.planDraft(set.ID(), c.DependsOn.SortedIDs()))

	h := Handle{Resource: set, name: name, members: map[string]resource.Resource{}}
	for _, m := range sp.members {
		r := resource.Register("ConfigSetMember", memberName(name, m.key), memberApplier(sp.outcomes, name, m.key), set.ID())
		resource.RecordPlanDraft(memberDraft(r.ID(), name, m, set.ID()))
		h.members[m.key] = r
		h.keys = append(h.keys, m.key)
	}
	return h
}

// Ensure builds and applies a config set without registering it or its
// member handles (the direct counterpart of Present, used by tests and by
// callers composing their own resources). It runs with the production system
// operations and a throwaway outcome store, since no member handle reads it.
func Ensure(name string, opts ...opt.ConfigSetOption) error {
	return ensure(name, newSystem(), newOutcomeStore(), opts)
}

// ensure is Ensure with the system operations and the outcome store passed in,
// so tests can inject failures and read the member outcomes of the apply.
func ensure(name string, sys *system, outcomes *outcomeStore, opts []opt.ConfigSetOption) error {
	c, err := build(name, opts)
	if err != nil {
		return err
	}
	c.spec.sys, c.spec.outcomes = sys, outcomes
	return c.spec.apply()
}
