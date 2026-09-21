package plan

// ConfigMember is one file of a KindConfigSet op (schema v21). ContentB64 is
// the member's bytes as rendered once on the controller. It may contain
// gonf-owned member-path tokens (resource/configset.MemberPath and
// MemberChrootPath); destination apply substitutes them with the staged
// candidate paths for validation and with the live paths for publication.
// Unlike a file op, a member never carries a blob reference: a config set is
// validated and published as a whole from the plan line itself.
type ConfigMember struct {
	// Key names the member inside its set (e.g. "aliases"); it is the
	// member handle's identity and the token key other members reference.
	Key string `json:"key"`
	// Path is the member's absolute live path.
	Path string `json:"path"`
	// ContentB64 is the base64 member content. Empty content is legitimate.
	ContentB64 string `json:"content_b64,omitempty"`
	// Mode is the octal permission string the published file carries.
	Mode string `json:"mode,omitempty"`
	// Owner and Group are the explicitly configured ownership; empty means
	// the applying user's default, exactly as for a file op.
	Owner string `json:"owner,omitempty"`
	Group string `json:"group,omitempty"`
}

// Argv is one directly executed command (never a shell string): Bin plus its
// arguments. KindConfigSet validators use it; member-path tokens inside Args
// are replaced with staged candidate paths before execution.
type Argv struct {
	Bin  string   `json:"bin"`
	Args []string `json:"args,omitempty"`
}
