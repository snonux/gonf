package user

import (
	"reflect"
	"strings"
	"testing"
)

func TestDesiredUserGroups(t *testing.T) {
	tests := []struct {
		name string
		user DesiredUser
		want []string
	}{
		{
			name: "sorts and deduplicates all groups",
			user: DesiredUser{
				PrimaryGroup:        "staff",
				SupplementaryGroups: []string{"wheel", "staff", "audio", "wheel"},
			},
			want: []string{"audio", "staff", "wheel"},
		},
		{
			name: "empty groups stay nil",
			user: DesiredUser{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.user.Groups(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Groups() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDesiredUserSupplementary(t *testing.T) {
	u := DesiredUser{
		PrimaryGroup:        "staff",
		SupplementaryGroups: []string{"wheel", "staff", "audio", "wheel"},
	}
	if got, want := u.Supplementary(), []string{"audio", "wheel"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Supplementary() = %v, want %v", got, want)
	}
}

func TestDesiredUserValidate(t *testing.T) {
	tests := []struct {
		name string
		user DesiredUser
		want string
	}{
		{"valid", DesiredUser{Name: "svc", Home: "/var/lib/svc", Shell: "/sbin/nologin"}, ""},
		{"empty user", DesiredUser{}, "user name is empty"},
		{"spaced user", DesiredUser{Name: "bad user"}, "user name"},
		{"flag user", DesiredUser{Name: "-bad"}, "starts with -"},
		{"comma primary group", DesiredUser{Name: "svc", PrimaryGroup: "bad,group"}, "comma"},
		{"comma supplementary group", DesiredUser{Name: "svc", SupplementaryGroups: []string{"bad,group"}}, "comma"},
		{"nul group", DesiredUser{Name: "svc", PrimaryGroup: "bad\x00group"}, "primary group"},
		{"empty supplementary", DesiredUser{Name: "svc", SupplementaryGroups: []string{""}}, "supplementary group name is empty"},
		{"nul home", DesiredUser{Name: "svc", Home: "/bad\x00home"}, "home contains NUL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.user.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want %q", err, tt.want)
			}
		})
	}
}
