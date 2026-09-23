package api

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/dir"
)

// writeGlobPruneFile writes body at path, creating its parent directories.
func writeGlobPruneFile(t *testing.T, path, body string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	mustWrite(t, path, []byte(body))
}

// globPruneEntry is one destination entry of a tree snapshot: its kind
// ("file", "dir", "link") and its content or raw link target.
type globPruneEntry struct {
	kind string
	data string
}

// writeGlobPruneSource creates the recipe's source directory for the sb2
// tests: counting matches (a script, a dot-named script, a template, a
// symlink to a regular file), a non-matching file for narrower patterns, and
// non-counting entries (a directory, a dangling symlink).
func writeGlobPruneSource(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "scripts")
	mustMkdirAll(t, filepath.Join(src, "nested"))
	for name, body := range map[string]string{
		"a.sh":           "new a\n",
		".hidden.sh":     "hidden\n",
		"param.sh.tmpl":  "param={{.Param}}\n",
		"readme.txt":     "readme\n",
		"nested/deep.sh": "deep\n",
		"target-of-link": "linked content\n",
	} {
		writeGlobPruneFile(t, filepath.Join(src, name), body)
	}
	mustSymlink(t, "target-of-link", filepath.Join(src, "linked.sh"))
	mustSymlink(t, "nowhere", filepath.Join(src, "dangling.sh"))
	return src
}

// writeGlobPruneDest fills dst with a host's pre-existing state: a stale
// managed file, unmanaged regular files (plain and dot-named), a file whose
// name matches only a non-counting source entry, an unmanaged subdirectory
// with content, and unmanaged symlinks (to a file and to a directory).
func writeGlobPruneDest(t *testing.T, dst string) {
	t.Helper()
	mustMkdirAll(t, filepath.Join(dst, "keepdir", "inner"))
	for name, body := range map[string]string{
		"a.sh":                 "stale a\n",
		"old.txt":              "unmanaged\n",
		".dotstale":            "unmanaged dot\n",
		"nested":               "file named like a source dir\n",
		"dangling.sh":          "file named like a dangling source link\n",
		"keepdir/inner/data":   "precious\n",
		"keepdir/unmatched.sh": "precious too\n",
	} {
		writeGlobPruneFile(t, filepath.Join(dst, name), body)
	}
	mustSymlink(t, "a.sh", filepath.Join(dst, "userlink"))
	mustSymlink(t, "keepdir", filepath.Join(dst, "dirlink"))
}

// snapshotTree maps every entry below root (relative path) to its kind and
// content or raw link target, never following symlinks.
func snapshotTree(t *testing.T, root string) map[string]globPruneEntry {
	t.Helper()
	out := map[string]globPruneEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == root {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			out[rel] = globPruneEntry{"link", target}
			return err
		case d.IsDir():
			out[rel] = globPruneEntry{"dir", ""}
		default:
			data, err := os.ReadFile(path)
			out[rel] = globPruneEntry{"file", string(data)}
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// recordGlobSync records a plan whose only resource is
// Dir(dst, WithSourceGlob(pattern), extra...), into planDir, and returns its
// ops after an encode/decode round trip (the wire a destination reads).
func recordGlobSync(t *testing.T, planDir, dst, pattern string, extra ...options.DirOption) []plan.Op {
	t.Helper()
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.ResetForTest()
	})
	opts := append([]options.DirOption{options.WithSourceGlob(pattern)}, extra...)
	Task("glob_sync", "", func() { Dir(dst, opts...) })
	ops, err := RecordPlan("sb2", planDir, "glob_sync")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	encoded, err := plan.EncodePlan(ops)
	if err != nil {
		t.Fatalf("EncodePlan: %v", err)
	}
	decoded, err := plan.DecodePlanBytes(encoded)
	if err != nil {
		t.Fatalf("DecodePlanBytes: %v", err)
	}
	return decoded
}

// syncDirOpOf returns the single sync_dir op of ops.
func syncDirOpOf(t *testing.T, ops []plan.Op) plan.Op {
	t.Helper()
	for _, op := range ops {
		if op.Op == plan.KindSyncDir {
			return op
		}
	}
	t.Fatalf("no sync_dir op in %#v", ops)
	return plan.Op{}
}

// syncDirPayloadOf returns op's SyncDirPayload (task 9e2 moved
// FileMode/SourceDir/Glob off plan.Op onto it). A comma-ok assertion,
// degrading to the zero payload for a non-sync_dir op or one decoded
// without a Payload — never a bare assertion that would panic.
func syncDirPayloadOf(op plan.Op) plan.SyncDirPayload {
	p, _ := op.Payload.(plan.SyncDirPayload)
	return p
}

// TestPlanGlobSyncPruneMatchesDirectPath is the sb2 regression: a Dir with
// WithSourceGlob and WithPrune applied through the plan path (record →
// encode → decode → plan.Apply) must leave the destination exactly as the
// direct path (dir.Ensure → pruneGlob) does. Before the fix the plan path
// rebuilt the flattened glob blob as a TREE sync, whose prune deleted the
// unmanaged subdirectory "keepdir" (with its content) and the unmanaged
// symlinks: data loss for e.g. the dotfiles' SyncDir(Home("scripts"), ...,
// WithPrune).
func TestPlanGlobSyncPruneMatchesDirectPath(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		// want pins the entries the rule decides, independent of the
		// direct-path comparison; nil kind means the entry must be gone.
		want map[string]*globPruneEntry
	}{
		{
			name:    "all entries",
			pattern: "*",
			want: map[string]*globPruneEntry{
				"a.sh":                 {"file", "new a\n"},
				".hidden.sh":           {"file", "hidden\n"},
				"readme.txt":           {"file", "readme\n"},
				"linked.sh":            {"file", "linked content\n"},
				"keepdir/inner/data":   {"file", "precious\n"},
				"keepdir/unmatched.sh": {"file", "precious too\n"},
				"userlink":             {"link", "a.sh"},
				"dirlink":              {"link", "keepdir"},
				"old.txt":              nil,
				".dotstale":            nil,
				"nested":               nil,
				"dangling.sh":          nil,
			},
		},
		{
			name:    "scripts only",
			pattern: "*.sh",
			want: map[string]*globPruneEntry{
				"a.sh":                 {"file", "new a\n"},
				".hidden.sh":           {"file", "hidden\n"},
				"linked.sh":            {"file", "linked content\n"},
				"keepdir/inner/data":   {"file", "precious\n"},
				"keepdir/unmatched.sh": {"file", "precious too\n"},
				"userlink":             {"link", "a.sh"},
				"dirlink":              {"link", "keepdir"},
				"readme.txt":           nil,
				"old.txt":              nil,
				"nested":               nil,
				"dangling.sh":          nil,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := writeGlobPruneSource(t)
			pattern := filepath.Join(src, tc.pattern)
			direct := filepath.Join(t.TempDir(), "direct")
			viaPlan := filepath.Join(t.TempDir(), "plan")
			writeGlobPruneDest(t, direct)
			writeGlobPruneDest(t, viaPlan)

			resource.ResetRepository()
			if err := dir.Ensure(direct, options.WithSourceGlob(pattern), options.WithPrune); err != nil {
				t.Fatalf("direct Ensure: %v", err)
			}

			planDir := t.TempDir()
			ops := recordGlobSync(t, planDir, viaPlan, pattern, options.WithPrune)
			if op := syncDirOpOf(t, ops); !syncDirPayloadOf(op).Glob || !op.Prune {
				t.Fatalf("sync_dir op = %#v, want glob and prune set", op)
			}
			if ops[0].Version != plan.VersionSyncDirGlob {
				t.Fatalf("header version = %d, want %d (older destinations must refuse a pruning glob sync)",
					ops[0].Version, plan.VersionSyncDirGlob)
			}
			if err := plan.Apply(ops, plan.Facts{}, planDir); err != nil {
				t.Fatalf("plan.Apply: %v", err)
			}

			got, wantTree := snapshotTree(t, viaPlan), snapshotTree(t, direct)
			if !maps.Equal(got, wantTree) {
				t.Fatalf("plan path tree differs from direct path:\n plan:   %v\n direct: %v", got, wantTree)
			}
			assertGlobPruneEntries(t, got, tc.want)
			// The template renders the recipe's declared source path on both
			// paths, never the ephemeral blob path.
			wantParam := globPruneEntry{"file", "param=" + filepath.Join(src, "param.sh.tmpl") + "\n"}
			if tc.pattern == "*" && got["param.sh"] != wantParam {
				t.Fatalf("param.sh = %v, want %v", got["param.sh"], wantParam)
			}
		})
	}
}

// assertGlobPruneEntries checks snapshot against want (nil = must be gone).
func assertGlobPruneEntries(t *testing.T, snapshot map[string]globPruneEntry, want map[string]*globPruneEntry) {
	t.Helper()
	for _, rel := range slices.Sorted(maps.Keys(want)) {
		got, ok := snapshot[rel]
		switch w := want[rel]; {
		case w == nil && ok:
			t.Errorf("%s = %v, want it pruned", rel, got)
		case w != nil && (!ok || got != *w):
			t.Errorf("%s = %v (present %t), want %v", rel, got, ok, *w)
		}
	}
}

// TestPlanGlobSyncWithoutPruneKeepsOldHeader pins the on-demand schema: a
// glob sync without prune installs the same files under tree and glob
// semantics, so it records glob but keeps the v21 header an older
// destination still applies, and leaves every unmanaged entry alone.
func TestPlanGlobSyncWithoutPruneKeepsOldHeader(t *testing.T) {
	src := writeGlobPruneSource(t)
	dst := filepath.Join(t.TempDir(), "dst")
	writeGlobPruneDest(t, dst)
	planDir := t.TempDir()
	ops := recordGlobSync(t, planDir, dst, filepath.Join(src, "*.sh"))
	if op := syncDirOpOf(t, ops); !syncDirPayloadOf(op).Glob || op.Prune {
		t.Fatalf("sync_dir op = %#v, want glob without prune", op)
	}
	if ops[0].Version != plan.VersionConfigSet {
		t.Fatalf("header version = %d, want %d", ops[0].Version, plan.VersionConfigSet)
	}
	if err := plan.Apply(ops, plan.Facts{}, planDir); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}
	assertGlobPruneEntries(t, snapshotTree(t, dst), map[string]*globPruneEntry{
		"a.sh":      {"file", "new a\n"},
		"old.txt":   {"file", "unmanaged\n"},
		".dotstale": {"file", "unmanaged dot\n"},
		"keepdir":   {"dir", ""},
	})
}

// TestPlanTreeSyncPruneKeepsTreeSemantics is the negative side: a WithSource
// tree sync is not marked glob, keeps the old header, and its prune still
// removes every destination entry without a source counterpart,
// subdirectories included (only the glob flavor changed).
func TestPlanTreeSyncPruneKeepsTreeSemantics(t *testing.T) {
	ResetTasks()
	resource.ResetRepository()
	t.Cleanup(func() {
		resource.SetPlanDraftRecorder(nil)
		plan.ResetForTest()
	})
	src := filepath.Join(t.TempDir(), "tree")
	writeGlobPruneFile(t, filepath.Join(src, "a.sh"), "new a\n")
	dst := filepath.Join(t.TempDir(), "dst")
	writeGlobPruneDest(t, dst)
	planDir := t.TempDir()
	Task("tree_sync", "", func() { Dir(dst, options.WithSource(src), options.WithPrune) })
	ops, err := RecordPlan("sb2-tree", planDir, "tree_sync")
	if err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if op := syncDirOpOf(t, ops); syncDirPayloadOf(op).Glob || ops[0].Version != plan.VersionConfigSet {
		t.Fatalf("tree sync op = %#v, header v%d; want no glob, v%d", op, ops[0].Version, plan.VersionConfigSet)
	}
	if err := plan.Apply(ops, plan.Facts{}, planDir); err != nil {
		t.Fatalf("plan.Apply: %v", err)
	}
	want := map[string]globPruneEntry{"a.sh": {"file", "new a\n"}}
	if got := snapshotTree(t, dst); !maps.Equal(got, want) {
		t.Fatalf("tree prune left %v, want %v", got, want)
	}
}
