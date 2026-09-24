package plan

import "encoding/json"

// wireOp is the flat, byte-for-byte JSON shape of one plan line: every field
// ever put on the wire, across every schema version, in the SAME
// declaration order and with the SAME json tags plan.Op itself carried
// before task yd2 ("Layer 2" of the PlanDraft/Op god-struct split — see
// docs/design/plan.md, "The PlanDraft/Op split"). Op's own Go-level shape now
// separates the fields two or more kinds share (its "core") from a
// per-kind OpPayload, the same rule task w62 ("Layer 1") applied to
// resource.PlanDraft; wireOp is what makes that split invisible on the
// wire.
//
// encoding/json always emits a struct's fields in declaration order, so
// keeping wireOp's order frozen — never reordered, only ever extended by
// appending a new field, exactly like Op's own history before this split —
// is what makes "a freshly encoded Op with unchanged values marshals to
// byte-identical JSON to before the split" a mechanical property of this
// type instead of something a merge step has to get right at runtime. Op's
// MarshalJSON (types.go) builds a wireOp from its core fields plus
// op.Payload.applyToWire, normalizes it (normalizeWire, below — the exact
// normalizeOp trimming this file's predecessor, plan/codec.go, used to do
// on Op directly) and marshals THAT; UnmarshalJSON is the mirror: decode
// into wireOp (a single json.Unmarshal reaches every field, core and
// payload alike, exactly as it always has), normalize, then split into
// Op's core fields plus a concrete OpPayload built by payloadFromWire.
//
// plan/testdata's golden fixtures round-trip through EncodePlan/DecodePlan
// byte-for-byte (TestGoldenDecodeReencode); plan/types_test.go's
// TestOpJSONTagsMatchPlanExamples additionally pins literal encoded bytes
// per example op. Both are the safety net for this type: touch it only to
// append a new field (with a schema version bump per docs/design/plan.md), never
// to reorder or remove one — a historical plan must keep decoding exactly
// as it always did.
type wireOp struct {
	Op      Kind   `json:"op"`
	Version int    `json:"version,omitempty"`
	ID      string `json:"id,omitempty"`

	Path string `json:"path,omitempty"`
	// Link-exclusive (plan.LinkPayload). See LinkPayload's own field docs.
	Symlink string `json:"symlink,omitempty"`
	// LinkIfExists-exclusive (plan.LinkIfExistsPayload). See its own field docs.
	Target string `json:"target,omitempty"`
	// Link-exclusive (plan.LinkPayload). See LinkPayload's own field docs.
	Hardlink string `json:"hardlink,omitempty"`

	Mode string `json:"mode,omitempty"`
	// SyncDir-exclusive (plan.SyncDirPayload). See its own field docs.
	FileMode string `json:"file_mode,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Group    string `json:"group,omitempty"`

	// ContentB64/HasContent/Template/TemplateParam/TemplateData/
	// ValidationBin/ValidationArgs below are File-exclusive
	// (plan.FilePayload, task ae2). Blob stays core: sync_dir reuses it
	// (see FilePayload's own field docs).
	ContentB64     string          `json:"content_b64,omitempty"`
	Blob           string          `json:"blob,omitempty"`
	HasContent     bool            `json:"has_content,omitempty"`
	Template       bool            `json:"template,omitempty"`
	TemplateParam  string          `json:"template_param,omitempty"`
	TemplateData   json.RawMessage `json:"template_data,omitempty"`
	ValidationBin  string          `json:"validation_bin,omitempty"`
	ValidationArgs []string        `json:"validation_args,omitempty"`
	// SyncDir-exclusive (plan.SyncDirPayload). See its own field docs.
	SourceDir string `json:"source_dir,omitempty"`
	// SyncDir-exclusive (plan.SyncDirPayload). See its own field docs.
	Glob bool `json:"glob,omitempty"`
	// Prune stays core (not payload): KindDir genuinely shares it with
	// KindSyncDir (see plan.Op's own Prune field doc for why).
	Prune  bool `json:"prune,omitempty"`
	Absent bool `json:"absent,omitempty"`
	// Package-exclusive (plan.PackagePayload). See its own field docs.
	Latest bool `json:"latest,omitempty"`

	// User-exclusive (plan.UserPayload). See UserPayload's own field docs.
	// This block's position is frozen like every other field in this
	// struct: task 6e2 moved these fields off Op onto UserPayload on the Go
	// side, but they stay declared HERE, at their original wire position,
	// because wireOp's declaration order is what the encoded byte stream
	// follows (see this type's own doc comment).
	PrimaryGroup        string   `json:"primary_group,omitempty"`
	SupplementaryGroups []string `json:"supplementary_groups,omitempty"`
	Home                string   `json:"home,omitempty"`
	CreateHome          bool     `json:"create_home,omitempty"`
	Shell               string   `json:"shell,omitempty"`
	LoginClass          string   `json:"login_class,omitempty"`
	System              bool     `json:"system,omitempty"`
	ManageHome          bool     `json:"manage_home,omitempty"`

	// AddLines/RemoveLines/KeyedLines/AddLine/RemoveLine are also
	// File-exclusive (plan.FilePayload, task ae2).
	AddLines    []string    `json:"add_lines,omitempty"`
	RemoveLines []string    `json:"remove_lines,omitempty"`
	KeyedLines  []KeyedLine `json:"keyed_lines,omitempty"`
	AddLine     string      `json:"add_line,omitempty"`
	RemoveLine  string      `json:"remove_line,omitempty"`

	// Name and Env are core (Name is a general label field, Env is shared
	// with KindPackage). Bin/Args/Dir below, and Creates/Unless/OnlyIf in
	// the next block, are Command-exclusive (plan.CommandPayload, task
	// 7e2). See CommandPayload's own field docs.
	Name string            `json:"name,omitempty"`
	Bin  string            `json:"bin,omitempty"`
	Args []string          `json:"args,omitempty"`
	Dir  string            `json:"dir,omitempty"`
	Env  map[string]string `json:"env,omitempty"`

	// Creates/Unless/OnlyIf are Command-exclusive (plan.CommandPayload).
	// See CommandPayload's own field docs.
	Creates string `json:"creates,omitempty"`
	Unless  *Guard `json:"unless,omitempty"`
	OnlyIf  *Guard `json:"only_if,omitempty"`

	// Cron-exclusive (plan.CronPayload). See CronPayload's own field docs.
	CronUser      string   `json:"cron_user,omitempty"`
	Command       string   `json:"command,omitempty"`
	LegacyCommand string   `json:"legacy_command,omitempty"`
	Schedule      string   `json:"schedule,omitempty"`
	CronEnv       []string `json:"cron_env,omitempty"`

	// SystemdTimer-exclusive (plan.SystemdTimerPayload). See its field docs.
	OnCalendar         string   `json:"on_calendar,omitempty"`
	OnBootSec          string   `json:"on_boot_sec,omitempty"`
	Persistent         bool     `json:"persistent,omitempty"`
	Description        string   `json:"description,omitempty"`
	ServiceDescription string   `json:"service_description,omitempty"`
	After              []string `json:"after,omitempty"`
	Wants              []string `json:"wants,omitempty"`

	User       bool     `json:"user,omitempty"`
	Restart    bool     `json:"restart,omitempty"`
	Reload     bool     `json:"reload,omitempty"`
	EnableOnly bool     `json:"enable_only,omitempty"`
	IfChanged  bool     `json:"if_changed,omitempty"`
	Watch      []string `json:"watch,omitempty"`

	Sensitive bool `json:"sensitive,omitempty"`
	Elevate   bool `json:"elevate,omitempty"`

	// ConfigSet-exclusive (plan.ConfigSetPayload): Members, Validators,
	// Chroot, StagingDir. ConfigSetMember-exclusive (plan.ConfigSetMemberPayload):
	// Member. Task 8e2 moved these off Op onto the two payload types (the
	// same two-kinds-one-package shape resource/configset's own
	// SetPayload/MemberPayload already has at Layer 1); this block's
	// position stays frozen like every other field in this struct — see
	// this type's own doc comment.
	Members    []ConfigMember `json:"members,omitempty"`
	Validators []Argv         `json:"validators,omitempty"`
	Chroot     string         `json:"chroot,omitempty"`
	StagingDir string         `json:"staging_dir,omitempty"`
	Member     string         `json:"member,omitempty"`

	Deps []string `json:"deps,omitempty"`

	All     []Predicate `json:"all,omitempty"`
	Require string      `json:"require,omitempty"`
}

// normalizeWire trims every wireOp slice from empty-but-non-nil to nil, so
// omitempty omits it and a decode-then-encode round trip is stable
// regardless of whether the caller (or an incoming JSON "x":[]) supplied an
// empty slice or none at all. This is the exact trimming plan/codec.go's
// normalizeOp did on the flat Op before task yd2; it now runs once, on the
// merged wire shape, so it covers core and (former, now-payload) exclusive
// fields alike without needing a per-payload copy of the same logic.
func normalizeWire(w *wireOp) {
	if len(w.Args) == 0 {
		w.Args = nil
	}
	if len(w.Env) == 0 {
		w.Env = nil
	}
	if len(w.All) == 0 {
		w.All = nil
	}
	if len(w.CronEnv) == 0 {
		w.CronEnv = nil
	}
	if len(w.SupplementaryGroups) == 0 {
		w.SupplementaryGroups = nil
	}
	if len(w.Deps) == 0 {
		w.Deps = nil
	}
	if len(w.AddLines) == 0 {
		w.AddLines = nil
	}
	if len(w.RemoveLines) == 0 {
		w.RemoveLines = nil
	}
	if len(w.KeyedLines) == 0 {
		w.KeyedLines = nil
	}
	if len(w.ValidationArgs) == 0 {
		w.ValidationArgs = nil
	}
	if w.Unless != nil {
		// Copy before normalizing: w.Unless may be a pointer shared with
		// other Op values referencing the same underlying Guard (e.g. every
		// per-host goroutine in a fleet/cluster push encodes its own copy of
		// the same ops slice — see internal/remote/fleet.go Fanout).
		// Normalizing in place would mutate that shared Guard through the
		// pointer, a data race under concurrent encoding. Encoding must stay
		// side-effect-free with respect to the caller's ops.
		g := *w.Unless
		normalizeGuard(&g)
		w.Unless = &g
	}
	if w.OnlyIf != nil {
		g := *w.OnlyIf
		normalizeGuard(&g)
		w.OnlyIf = &g
	}
}

func normalizeGuard(g *Guard) {
	if len(g.Args) == 0 {
		g.Args = nil
	}
}
