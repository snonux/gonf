package options_test

import (
	"slices"
	"testing"

	"github.com/snonux/gonf/resource/cmd"
	"github.com/snonux/gonf/resource/cron"
	"github.com/snonux/gonf/resource/dir"
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/link"
	"github.com/snonux/gonf/resource/options"
	"github.com/snonux/gonf/resource/pkg"
	"github.com/snonux/gonf/resource/service"
	"github.com/snonux/gonf/resource/systemd"
	"github.com/snonux/gonf/resource/systemdtimer"
	"github.com/snonux/gonf/resource/timer"
	"github.com/snonux/gonf/resource/user"
)

// realResources maps each option family to the concrete resource type its
// constructor applies the options to. It lives in this external test package
// because resource/options cannot import the resource packages (they import
// it).
var realResources = map[string]any{
	"File":         &file.File{},
	"Dir":          &dir.Dir{},
	"Link":         &link.Link{},
	"Package":      &pkg.Package{},
	"Service":      &service.Service{},
	"Cron":         &cron.Cron{},
	"Timer":        &timer.Timer{},
	"SystemdTimer": &systemdtimer.SystemdTimer{},
	"DaemonReload": &systemd.DaemonReloadResource{},
	"Command":      &cmd.Cmd{},
	"LocalUser":    &user.User{},
}

// TestRealResourcesImplementTheirFamilies closes the gap between the
// compile-time family types and the runtime capability check: an option
// accepted by a family compiles against that resource's constructor, but
// aborts with "does not support" if the concrete type lacks the setter. For
// every family and every option it accepts, the real resource type must
// implement the exact setter the option calls (see options.MissingSetters).
func TestRealResourcesImplementTheirFamilies(t *testing.T) {
	for _, fam := range options.Families() {
		t.Run(fam, func(t *testing.T) {
			res, ok := realResources[fam]
			if !ok {
				t.Fatalf("no real resource type registered for family %s", fam)
			}
			for _, msg := range options.MissingSetters(fam, res) {
				t.Error(msg)
			}
		})
	}
	for fam := range realResources {
		if !slices.Contains(options.Families(), fam) {
			t.Errorf("realResources entry %s is not an option family", fam)
		}
	}
}
