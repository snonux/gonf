package plan

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// goldenPlans lists checked-in JSONL fixtures under testdata/, one per plan
// example family from the remote-gonf design plan.
var goldenPlans = []string{
	"home_bash_vale.jsonl",
	"home_taskwarrior.jsonl",
	"home_agents.jsonl",
	"link_if_exists.jsonl",
	"home_systemd_user.jsonl",
	"pkg_fedora.jsonl",
	"mini_e2e.jsonl",
	"change_gate.jsonl",
}

func TestGoldenDecodeReencode(t *testing.T) {
	t.Parallel()
	for _, name := range goldenPlans {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("testdata", name)
			wantRaw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}

			ops, err := DecodePlanBytes(wantRaw)
			if err != nil {
				t.Fatalf("DecodePlanBytes: %v", err)
			}
			if len(ops) == 0 || ops[0].Op != KindPlan {
				t.Fatalf("expected plan header, got %#v", ops)
			}

			gotRaw, err := EncodePlan(ops)
			if err != nil {
				t.Fatalf("EncodePlan: %v", err)
			}
			if !bytes.Equal(gotRaw, wantRaw) {
				t.Fatalf("re-encode mismatch\n--- want ---\n%s\n--- got ---\n%s", wantRaw, gotRaw)
			}

			// Decode again and deep-equal to ensure encode did not drop fields.
			ops2, err := DecodePlanBytes(gotRaw)
			if err != nil {
				t.Fatalf("second DecodePlanBytes: %v", err)
			}
			if !reflect.DeepEqual(ops2, ops) {
				t.Fatalf("ops not stable after re-encode\ngot  %#v\nwant %#v", ops2, ops)
			}
		})
	}
}

func TestGoldenFixturesCoverInventory(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("readdir testdata: %v", err)
	}
	found := map[string]bool{}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".jsonl" {
			found[e.Name()] = true
		}
	}
	for _, name := range goldenPlans {
		if !found[name] {
			t.Errorf("missing golden fixture %s", name)
		}
	}
}
