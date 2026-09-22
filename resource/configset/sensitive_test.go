package configset

import (
	"testing"

	opt "github.com/snonux/gonf/resource/options"
)

// WithSensitive marks the whole set, whether it is given to the set or to
// one member (a ConfigFile option): the set's one op carries every member,
// and its validators see them all. Without it the set stays unmarked.
func TestWithSensitiveMarksTheWholeSet(t *testing.T) {
	validate := opt.WithSetValidation("/bin/true", []string{opt.MemberPath("a")})
	tests := []struct {
		name string
		opts []opt.ConfigSetOption
		want bool
	}{
		{"unmarked", []opt.ConfigSetOption{
			opt.ConfigFile("a", "/etc/svc/a", opt.WithContent("x")), validate}, false},
		{"set", []opt.ConfigSetOption{
			opt.ConfigFile("a", "/etc/svc/a", opt.WithContent("x")), validate, opt.WithSensitive}, true},
		{"member", []opt.ConfigSetOption{
			opt.ConfigFile("a", "/etc/svc/a", opt.WithContent("x")),
			opt.ConfigFile("b", "/etc/svc/b", opt.WithContent("y"), opt.WithSensitive), validate}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := build("svc", tt.opts)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if c.spec.sensitive != tt.want || c.spec.planDraft("ConfigSet[svc]", nil).Sensitive != tt.want {
				t.Fatalf("spec.sensitive = %v, draft.Sensitive = %v, want %v",
					c.spec.sensitive, c.spec.planDraft("ConfigSet[svc]", nil).Sensitive, tt.want)
			}
		})
	}
}
