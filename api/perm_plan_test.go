package api

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
)

// permSpelling is one way of writing "mode 0o750, owner svc, group staff" (or
// the Root equivalent): the combined Perm option or the three separate ones.
// Both option families are built so one spelling serves every kind.
type permSpelling struct {
	file []options.FileOption
	dir  []options.DirOption
}

// permSpellings returns the combined and the three-option spelling of mode
// plus the owner spec usr:group (group "0" is what Root records).
func permSpellings(spec, usr, group string) (combined, separate permSpelling) {
	combined = permSpelling{
		file: []options.FileOption{options.Perm(0o750, spec)},
		dir:  []options.DirOption{options.Perm(0o750, spec)},
	}
	separate = permSpelling{
		file: []options.FileOption{options.WithMode(0o750), options.WithOwner(usr), options.WithGroup(group)},
		dir:  []options.DirOption{options.WithMode(0o750), options.WithOwner(usr), options.WithGroup(group)},
	}
	return combined, separate
}

// permKinds declares one resource of every kind (and api wrapper) that
// accepts Perm, each carrying the spelling's options. src is a controller
// directory holding the "app.conf" source InstallFile and SyncDir copy.
func permKinds(src string) map[string]func(permSpelling) {
	return map[string]func(permSpelling){
		"File":        func(p permSpelling) { File("/etc/app.conf", append(p.file, options.WithContent("x\n"))...) },
		"EnsureFile":  func(p permSpelling) { EnsureFile("/etc/app.conf", p.file...) },
		"InstallFile": func(p permSpelling) { InstallFile("/etc/app.conf", filepath.Join(src, "app.conf"), p.file...) },
		"SecretFile":  func(p permSpelling) { SecretFile("/etc/app.key", "svc/key", p.file...) },
		"Dir":         func(p permSpelling) { Dir("/srv/app", p.dir...) },
		"EnsureDir":   func(p permSpelling) { EnsureDir("/srv/app", p.dir...) },
		"SyncDir":     func(p permSpelling) { SyncDir("/srv/app", filepath.Join(src, "*"), p.dir...) },
		"ConfigFile": func(p permSpelling) {
			ConfigSet("app", options.ConfigFile("conf", "/etc/app/conf", append(p.file, options.WithContent("x\n"))...),
				options.WithSetValidation("true", []string{options.MemberPath("conf")}))
		},
	}
}

// recordPermPlan records body as task "t" and returns the encoded plan.
func recordPermPlan(t *testing.T, body func()) []byte {
	t.Helper()
	ResetForTest()
	Task("t", "", body)
	ops, err := RecordPlanTo("perm", plan.NewMemoryStore(), "t")
	if err != nil {
		t.Fatalf("RecordPlanTo: %v", err)
	}
	raw, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestPermRecordsTheThreeOptionPlan pins that Perm is pure sugar: for every
// kind accepting it, Perm(mode, "user:group"), Perm(mode, ":group") and
// Perm(mode, Root) record plans byte-identical to the WithMode + WithOwner +
// WithGroup spelling (Root's being WithOwner("root") + WithGroup("0")), so
// neither Perm nor Root changes the schema or the plan version, and a
// destination running an older gonf applies them unchanged. Not parallel:
// it changes the working directory and resets the process-global DSL.
func TestPermRecordsTheThreeOptionPlan(t *testing.T) {
	useSecretWorkDir(t)
	writeSecret(t, "svc/key", "fake-perm-key\n")
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "app.conf"), []byte("src\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetForTest)

	specs := []struct{ spec, usr, group string }{
		{"svc:staff", "svc", "staff"},
		{options.Root, "root", "0"},
	}
	for kind, declare := range permKinds(src) {
		for _, s := range specs {
			combined, separate := permSpellings(s.spec, s.usr, s.group)
			got := recordPermPlan(t, func() { declare(combined) })
			want := recordPermPlan(t, func() { declare(separate) })
			// Guard against a vacuous pass: the ownership must be on the wire.
			if !bytes.Contains(got, []byte(`"group":"`+s.group+`"`)) {
				t.Errorf("%s with Perm(0o750, %q) recorded no group %q:\n%s", kind, s.spec, s.group, got)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s with Perm(0o750, %q) plan differs from the three-option form:\n got %s\nwant %s", kind, s.spec, got, want)
			}
		}
		// ":group" is WithMode + WithGroup alone: no owner is recorded.
		got := recordPermPlan(t, func() {
			declare(permSpelling{file: []options.FileOption{options.Perm(0o750, ":staff")}, dir: []options.DirOption{options.Perm(0o750, ":staff")}})
		})
		want := recordPermPlan(t, func() {
			declare(permSpelling{
				file: []options.FileOption{options.WithMode(0o750), options.WithGroup("staff")},
				dir:  []options.DirOption{options.WithMode(0o750), options.WithGroup("staff")},
			})
		})
		if !bytes.Equal(got, want) {
			t.Errorf("%s with Perm(0o750, \":staff\") plan differs from WithMode + WithGroup:\n got %s\nwant %s", kind, got, want)
		}
	}
}

// TestPermMalformedOwnerFailsTheRecord pins the misuse contract end to end:
// a malformed owner spec fails the record with a declaration error instead
// of recording a plan or ending the process.
func TestPermMalformedOwnerFailsTheRecord(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	Task("t", "", func() { File("/etc/app.conf", options.WithContent("x\n"), options.Perm(0o644, "root:")) })
	if _, err := RecordPlanTo("perm", plan.NewMemoryStore(), "t"); err == nil {
		t.Fatal("RecordPlanTo accepted Perm(0o644, \"root:\")")
	}
}
