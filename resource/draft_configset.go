package resource

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

// PlanArgv is a package-neutral argv command: Bin plus its arguments.
type PlanArgv struct {
	Bin  string
	Args []string
}
