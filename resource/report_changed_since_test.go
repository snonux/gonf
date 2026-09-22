package resource_test

import (
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestChangedSince pins the daemon-reload coalescing oracle: only changes
// noted after the anchor's last change count, a missing (or merely skipped
// or OK) anchor degrades to AnyChanged, and the Directory rule still
// applies to the remaining notes.
func TestChangedSince(t *testing.T) {
	const anchor = "DaemonReload[user]"
	type n struct {
		id string
		st resource.Status
	}
	for _, tc := range []struct {
		name  string
		notes []n
		watch []string
		want  bool
	}{
		{name: "no anchor is AnyChanged",
			notes: []n{{"File[a]", resource.StatusChanged}},
			watch: []string{"File[a]"}, want: true},
		{name: "change before the anchor is loaded",
			notes: []n{{"File[a]", resource.StatusChanged}, {anchor, resource.StatusChanged}},
			watch: []string{"File[a]"}, want: false},
		{name: "change after the anchor counts",
			notes: []n{{anchor, resource.StatusChanged}, {"File[a]", resource.StatusChanged}},
			watch: []string{"File[a]"}, want: true},
		{name: "last anchor wins",
			notes: []n{{anchor, resource.StatusChanged}, {"File[a]", resource.StatusChanged}, {anchor, resource.StatusChanged}},
			watch: []string{"File[a]"}, want: false},
		{name: "skipped anchor is no reload",
			notes: []n{{"File[a]", resource.StatusChanged}, {anchor, resource.StatusSkipped}},
			watch: []string{"File[a]"}, want: true},
		{name: "would-change anchor is a dry-run reload",
			notes: []n{{"File[a]", resource.StatusWouldChange}, {anchor, resource.StatusWouldChange}},
			watch: []string{"File[a]"}, want: false},
		{name: "other bus does not anchor",
			notes: []n{{"File[a]", resource.StatusChanged}, {"DaemonReload[system]", resource.StatusChanged}},
			watch: []string{"File[a]"}, want: true},
		{name: "directory child after the anchor counts",
			notes: []n{{anchor, resource.StatusChanged}, {"File[/u/x.timer]", resource.StatusChanged}},
			watch: []string{"Directory[/u]"}, want: true},
		{name: "directory child before the anchor is loaded",
			notes: []n{{"File[/u/x.timer]", resource.StatusChanged}, {anchor, resource.StatusChanged}, {"File[/other]", resource.StatusChanged}},
			watch: []string{"Directory[/u]"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource.ResetReport()
			t.Cleanup(resource.ResetReport)
			for _, x := range tc.notes {
				resource.Note(x.id, x.st)
			}
			if got := resource.ChangedSince(anchor, tc.watch...); got != tc.want {
				t.Fatalf("ChangedSince(%s, %v) = %v, want %v", anchor, tc.watch, got, tc.want)
			}
		})
	}
}
