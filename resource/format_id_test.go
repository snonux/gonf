package resource

import "testing"

// TestFormatIDSpelling pins the one "Type[Name]" definition (task 272) and
// that idPrefix is its prefix, so the report's directory matching parses
// exactly what FormatID builds.
func TestFormatIDSpelling(t *testing.T) {
	for _, tc := range []struct{ typ, name, want string }{
		{"File", "/etc/foo", "File[/etc/foo]"},
		{"Cron", "root/backup", "Cron[root/backup]"},
		{"Group", "", "Group[]"},
		{"User", "a[b]", "User[a[b]]"},
	} {
		got := FormatID(tc.typ, tc.name)
		if got != tc.want {
			t.Errorf("FormatID(%q, %q) = %q, want %q", tc.typ, tc.name, got, tc.want)
		}
		if got[:len(idPrefix(tc.typ))] != idPrefix(tc.typ) {
			t.Errorf("idPrefix(%q) = %q is not a prefix of %q", tc.typ, idPrefix(tc.typ), got)
		}
		if r := (Resource{Type: tc.typ, Name: tc.name}); r.ID() != got {
			t.Errorf("Resource.ID() = %q, want FormatID's %q", r.ID(), got)
		}
	}
	if path, ok := directoryNotePath(FormatID("Directory", "/x/conf.d")); !ok || path != "/x/conf.d" {
		t.Errorf("directoryNotePath(FormatID(Directory, /x/conf.d)) = %q, %v", path, ok)
	}
}
