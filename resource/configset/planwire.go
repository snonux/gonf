package configset

import (
	"encoding/base64"
	"fmt"
	"slices"

	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	opt "github.com/snonux/gonf/resource/options"
)

// setHandler owns the config_set wire form; memberHandler owns the
// report-only config_set_member handle. Applying a set op records the member
// outcomes in outcomes, and applying a member op reads them from there, so a
// pair works only when both share one store (newHandlers). sys is the system
// operations a set op is applied with. ToOp needs neither field.
type (
	setHandler struct {
		sys      *system
		outcomes *outcomeStore
	}
	memberHandler struct {
		outcomes *outcomeStore
	}
)

func init() {
	set, member := newHandlers(newSystem())
	plan.RegisterHandler(plan.KindConfigSet, set)
	plan.RegisterHandler(plan.KindConfigSetMember, member)
}

// newHandlers returns a set and a member handler sharing a new outcome store.
// init registers the production pair; tests build their own with an injected
// system.
func newHandlers(sys *system) (setHandler, memberHandler) {
	outcomes := newOutcomeStore()
	return setHandler{sys: sys, outcomes: outcomes}, memberHandler{outcomes: outcomes}
}

// planDraft is the package-neutral record of the set.
func (s *spec) planDraft(id string, deps []string) resource.PlanDraft {
	d := resource.PlanDraft{
		Kind:       string(plan.KindConfigSet),
		ID:         id,
		Name:       s.name,
		Chroot:     s.chroot,
		StagingDir: s.stagingDir,
		Deps:       deps,
	}
	for _, m := range s.members {
		pm := resource.PlanConfigMember{
			Key: m.key, Path: m.path, Content: slices.Clone(m.content),
			Mode: opt.ModeToWire(m.mode), Owner: m.owner, Group: m.group,
		}
		d.ConfigMembers = append(d.ConfigMembers, pm)
	}
	for _, v := range s.validators {
		d.Validators = append(d.Validators, resource.PlanArgv{Bin: v.Bin, Args: slices.Clone(v.Args)})
	}
	return d
}

// memberDraft is the record of one member handle; it depends on the set.
func memberDraft(id, name string, m memberSpec, set string) resource.PlanDraft {
	return resource.PlanDraft{
		Kind:   string(plan.KindConfigSetMember),
		ID:     id,
		Name:   name,
		Member: m.key,
		Path:   m.path,
		Deps:   []string{set},
	}
}

// ToOp lowers a set draft. The spec is re-validated here so a malformed set
// fails `gonf plan` on the controller, and members larger than the inline
// limit are refused: a set is published from its plan line, never from blobs.
func (setHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	op := plan.Op{
		Op: plan.KindConfigSet, ID: d.ID, Name: d.Name,
		Chroot: d.Chroot, StagingDir: d.StagingDir, Deps: d.Deps,
	}
	for _, m := range d.ConfigMembers {
		if len(m.Content) > plan.MaxInlineContent {
			return plan.Op{}, fmt.Errorf("config set %s: member %s exceeds the %d byte inline limit", d.Name, m.Key, plan.MaxInlineContent)
		}
		op.Members = append(op.Members, plan.ConfigMember{
			Key: m.Key, Path: m.Path, ContentB64: base64.StdEncoding.EncodeToString(m.Content),
			Mode: m.Mode, Owner: m.Owner, Group: m.Group,
		})
	}
	for _, v := range d.Validators {
		op.Validators = append(op.Validators, plan.Argv{Bin: v.Bin, Args: slices.Clone(v.Args)})
	}
	if _, err := specFromOp(op); err != nil {
		return plan.Op{}, err
	}
	return op, nil
}

// Apply rebuilds the spec from the op, validates it before any mutation, and
// runs the same apply as a direct recipe, with the handler's system and
// outcome store.
func (h setHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	s, err := specFromOp(op)
	if err != nil {
		return err
	}
	s.sys, s.outcomes = h.sys, h.outcomes
	return s.apply()
}

// specFromOp decodes and validates a config_set op.
func specFromOp(op plan.Op) (*spec, error) {
	if op.Name == "" {
		return nil, fmt.Errorf("config_set: missing name")
	}
	s := &spec{name: op.Name, chroot: op.Chroot, stagingDir: op.StagingDir}
	for _, m := range op.Members {
		ms, err := memberFromWire(m)
		if err != nil {
			return nil, fmt.Errorf("config set %s: member %s: %w", op.Name, m.Key, err)
		}
		s.members = append(s.members, ms)
	}
	for _, v := range op.Validators {
		s.validators = append(s.validators, resource.PlanArgv{Bin: v.Bin, Args: slices.Clone(v.Args)})
	}
	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// memberFromWire decodes one wire member. Empty content is legitimate (an
// empty table), so an empty content_b64 decodes to zero bytes rather than
// being refused like a file op without content. A missing mode falls back to
// the File default, matching a member recorded without WithMode.
func memberFromWire(m plan.ConfigMember) (memberSpec, error) {
	content, err := base64.StdEncoding.DecodeString(m.ContentB64)
	if err != nil {
		return memberSpec{}, fmt.Errorf("content_b64: %w", err)
	}
	mode := defaultMemberMode
	if m.Mode != "" {
		if mode, err = plan.ParseMode(m.Mode); err != nil {
			return memberSpec{}, err
		}
	}
	return memberSpec{key: m.Key, path: m.Path, content: content, mode: mode, owner: m.Owner, group: m.Group}, nil
}

// ToOp lowers a member handle draft.
func (memberHandler) ToOp(d resource.PlanDraft) (plan.Op, error) {
	return plan.Op{
		Op: plan.KindConfigSetMember, ID: d.ID, Name: d.Name,
		Member: d.Member, Path: d.Path, Deps: d.Deps,
	}, nil
}

// Apply notes the member handle's result from its set's apply, read from the
// store the paired setHandler recorded it in.
func (h memberHandler) Apply(op plan.Op, _ plan.ApplyContext) error {
	if op.Name == "" || op.Member == "" {
		return fmt.Errorf("config_set_member: missing set name or member key")
	}
	return applyMember(h.outcomes, op.Name, op.Member)
}
