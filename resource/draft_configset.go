package resource

import "slices"

// PlanConfigMember is the package-neutral draft form of one config-set
// member. Content holds the member bytes rendered once at record time
// (member-path tokens included); the config_set plan handler base64-encodes
// it onto the wire.
type PlanConfigMember struct {
	Key     string
	Path    string
	Content []byte
	// Mode is the octal permission string the published file carries.
	Mode string
	// Owner and Group are only set when explicitly configured.
	Owner string
	Group string
}

// ClonePlanConfigMembers deep-copies config-set members, including each
// member's Content bytes (nil stays nil, empty stays empty). Exported
// (task 2e2) so every kind package that embeds a []PlanConfigMember in its
// own DraftPayload (currently resource/configset's SetPayload) clones it
// through this one definition next to the type, instead of a private
// per-package copy: resource genuinely cannot reach INTO a kind package's
// payload type to clone it generically, but it can export a clone function
// for its own type here, which every consumer can call.
func ClonePlanConfigMembers(members []PlanConfigMember) []PlanConfigMember {
	if members == nil {
		return nil
	}
	c := make([]PlanConfigMember, len(members))
	for i, m := range members {
		m.Content = slices.Clone(m.Content)
		c[i] = m
	}
	return c
}

// PlanArgv is a package-neutral argv command: Bin plus its arguments.
type PlanArgv struct {
	Bin  string
	Args []string
}

// ClonePlanArgvs deep-copies argv commands, including each one's Args (nil
// stays nil, empty stays empty). Exported for the same reason as
// ClonePlanConfigMembers above.
func ClonePlanArgvs(argvs []PlanArgv) []PlanArgv {
	if argvs == nil {
		return nil
	}
	c := make([]PlanArgv, len(argvs))
	for i, a := range argvs {
		a.Args = slices.Clone(a.Args)
		c[i] = a
	}
	return c
}
