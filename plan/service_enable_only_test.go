package plan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/snonux/gonf/resource"
)

func TestServiceEnableOnlyPreflight(t *testing.T) {
	for _, absent := range []bool{false, true} {
		for _, runtime := range []Op{{Restart: true}, {Reload: true}, {IfChanged: true, Watch: []string{"File[gonfy]"}}} {
			runtime.Op = KindService
			runtime.Name = "gonfy"
			runtime.ID = "Service[gonfy]"
			runtime.EnableOnly = true
			runtime.Absent = absent
			path := filepath.Join(t.TempDir(), "must-not-exist")
			file := Op{Op: KindFile, ID: "File[gonfy]", Path: path, Payload: FilePayload{HasContent: true, ContentB64: "Z29uZnk="}}
			ops := []Op{header(), file, runtime}
			for _, dry := range []bool{false, true} {
				resource.SetDryRun(dry)
				defer resource.SetDryRun(false)
				var refusal Refusal
				if err := Apply(ops, Facts{}, ""); err == nil || !errors.As(err, &refusal) {
					t.Fatalf("apply refusal: %v", err)
				}
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("preceding file mutated: %v", err)
				}
			}
			var refusal Refusal
			if err := ValidateChunks([]Chunk{{Ops: []Op{header(), file}}, {Ops: []Op{header(), runtime}}}); err == nil || !errors.As(err, &refusal) {
				t.Fatalf("chunk refusal: %v", err)
			}
		}
	}
	if err := ValidateServiceEnableOnly([][]Op{{{Op: KindTimer, EnableOnly: true, Restart: true, IfChanged: true}, {Op: KindService, EnableOnly: true}, {Op: KindService, Restart: true}}}); err != nil {
		t.Fatal(err)
	}
}
