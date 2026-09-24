package options

import (
	"os"
	"strings"
	"testing"

	"github.com/snonux/gonf/internal/declerr"
	"github.com/snonux/gonf/resource"
)

// contentOnly is a resource with a single capability, standing in for a real
// resource that lacks the capability an erased option needs.
type contentOnly struct{}

func (*contentOnly) SetContent(string) {}

// watchOnly can arm a change gate but cannot record dependency edges, which
// OnChange needs as well.
type watchOnly struct{}

func (*watchOnly) SetChangeWatch([]string) {}

// misuseCases are option misuses that must be refused, each with a fragment
// of the message the user sees. apply runs the misuse against a fresh target(); a nil
// target stands for a call without one (NormalizeMode).
var misuseCases = []struct {
	name    string
	target  func() any
	apply   func(target any)
	wantMsg string
}{
	{"option on resource without capability", func() any { return struct{}{} }, func(t any) { WithOwner("x").Apply(t) }, "struct {} does not support WithOwner"},
	{"option on partially capable resource", func() any { return &contentOnly{} }, func(t any) { WithValidation("v", []string{CandidatePath}).Apply(t) }, "*options.contentOnly does not support WithValidation"},
	{"option on nil target", func() any { return nil }, func(any) { DependsOn(fileA).Apply(nil) }, "<nil> does not support DependsOn"},
	{"erased option through the wrong adapter", func() any { return &contentOnly{} }, func(t any) { ToFileOptions(WithCommand("true"))[0].Apply(t) }, "does not support WithCommand"},
	{"OnChange without resources", func() any { return &recorder{} }, func(t any) { OnChange().Apply(t) }, "OnChange requires at least one resource to watch"},
	{"OnChange with an empty Multi", func() any { return &recorder{} }, func(t any) { OnChange(resource.Multi(nil)).Apply(t) }, "OnChange requires at least one resource to watch"},
	{"OnChange on a target without dependencies", func() any { return &watchOnly{} }, func(t any) { OnChange(fileA).Apply(t) }, "*options.watchOnly does not support OnChange"},
	{"WatchChanges without ids", func() any { return &recorder{} }, func(t any) { WatchChanges().Apply(t) }, "WatchChanges requires at least one resource id"},
	{"WithCronUser empty", func() any { return &recorder{} }, func(t any) { WithCronUser("").Apply(t) }, "WithCronUser must not be empty"},
	{"WithCronUser single space", func() any { return &recorder{} }, func(t any) { WithCronUser(" ").Apply(t) }, "WithCronUser must not be empty"},
	{"WithCronUser tab", func() any { return &recorder{} }, func(t any) { WithCronUser("\t").Apply(t) }, "WithCronUser must not be empty"},
	{"IfChanged outside daemon-reload", func() any { return &watchOnly{} }, func(t any) { IfChanged.Apply(t) }, "*options.watchOnly does not support IfChanged"},
	{"WithWatch outside daemon-reload", func() any { return &watchOnly{} }, func(t any) { WithWatch("File[a]").Apply(t) }, "*options.watchOnly does not support WithWatch"},
	{"empty WithWatch outside daemon-reload", func() any { return &watchOnly{} }, func(t any) { WithWatch().Apply(t) }, "*options.watchOnly does not support WithWatch"},
	{"NormalizeMode above 0o7777", func() any { return nil }, func(any) { NormalizeMode(0o10000) }, "outside 0o7777"},
	{"WithMode with a type bit", func() any { return &recorder{} }, func(t any) { WithMode(os.ModeDir | 0o755).Apply(t) }, "outside 0o7777"},
	{"Perm with an empty owner", func() any { return &recorder{} }, func(t any) { Perm(0o644, "").Apply(t) }, "Perm owner is empty"},
	{"Perm with an empty group", func() any { return &recorder{} }, func(t any) { Perm(0o644, "root:").Apply(t) }, "has an empty group"},
	{"Perm with a lone colon", func() any { return &recorder{} }, func(t any) { Perm(0o644, ":").Apply(t) }, "has an empty group"},
	{"Perm with two colons", func() any { return &recorder{} }, func(t any) { Perm(0o644, "a:b:c").Apply(t) }, "more than one colon"},
	{"Perm with a type bit", func() any { return &recorder{} }, func(t any) { Perm(os.ModeDir|0o755, Root).Apply(t) }, "outside 0o7777"},
	{"WithOwner spec with an empty group", func() any { return &recorder{} }, func(t any) { WithOwner("root:").Apply(t) }, "WithOwner owner \"root:\" has an empty group"},
	{"Perm without the capability", func() any { return struct{}{} }, func(t any) { Perm(0o644, "x").Apply(t) }, "struct {} does not support Perm"},
	{"WithFileMode with a type bit", func() any { return &recorder{} }, func(t any) { WithFileMode(os.ModeSymlink | 0o644).Apply(t) }, "outside 0o7777"},
}

// TestOptionMisuseIsReported applies every misuse in-process (options never
// end the process) and checks where the refusal lands: a target that
// collects misuse (MisuseReporter, here the recorder) gets it and no setter
// runs, so the resource's build fails with it; any other target reports it
// as a declaration error (internal/declerr). Not parallel: declerr is
// process-global.
func TestOptionMisuseIsReported(t *testing.T) {
	for _, tc := range misuseCases {
		t.Run(tc.name, func(t *testing.T) {
			declerr.Reset()
			t.Cleanup(declerr.Reset)
			target := tc.target() // fresh per run, so -count=N starts clean
			tc.apply(target)
			got := reportedMisuse(t, target)
			if !strings.Contains(got, tc.wantMsg) {
				t.Fatalf("reported misuse = %q, want it to contain %q", got, tc.wantMsg)
			}
		})
	}
}

// reportedMisuse returns the misuse the case reported: the recorder's single
// ReportMisuse call (which must be its only call, and must leave declerr
// untouched), or the declaration error otherwise.
func reportedMisuse(t *testing.T, target any) string {
	t.Helper()
	r, ok := target.(*recorder)
	if !ok {
		err := declerr.First()
		if err == nil {
			t.Fatal("misuse reported no declaration error")
		}
		return err.Error()
	}
	if err := declerr.First(); err != nil {
		t.Fatalf("a MisuseReporter target's misuse also reached declerr: %v", err)
	}
	if len(r.calls) != 1 || r.calls[0].method != "ReportMisuse" {
		t.Fatalf("recorder calls = %v, want exactly one ReportMisuse and no setter", r.calls)
	}
	return r.calls[0].value.(string)
}

// TestNormalizeModeDropsInvalidBits: the standalone NormalizeMode reports the
// misuse and returns the mode without the unsupported bits, so a caller that
// keeps going never applies a file-type bit.
func TestNormalizeModeDropsInvalidBits(t *testing.T) {
	declerr.Reset()
	t.Cleanup(declerr.Reset)
	if got := NormalizeMode(os.ModeDir | 0o4755); got != os.ModeSetuid|0o755 {
		t.Fatalf("NormalizeMode = %v, want setuid|0755", got)
	}
	if declerr.First() == nil {
		t.Fatal("NormalizeMode with a type bit reported nothing")
	}
}
