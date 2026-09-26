package cron

import (
	"strings"
	"testing"

	opt "github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/runners"
)

// jobBlock renders the block of job "job" running /bin/true at minute past
// 1 o'clock with the given WithCronEnv lines.
func jobBlock(minute string, env ...string) string {
	c := &Cron{
		name: "job", user: "root", command: "/bin/true",
		minute: minute, hour: "1", monthday: "*", month: "*", weekday: "*",
		env: env,
	}
	return c.block()
}

// Regression (task ab): a changed block must stay where it was, so the
// NAME=value lines below it, which cron applies to every later entry, do
// not start applying to the job.
func TestMergeRewritesChangedBlockInPlace(t *testing.T) {
	head := "MAILTO=root\n\n0 * * * * /bin/echo before\n"
	tail := "PATH=/opt/other/bin\n0 * * * * /bin/echo after\n"

	for _, tc := range []struct {
		name     string
		old, new string
	}{
		{"no env", jobBlock("0"), jobBlock("5")},
		{"same env", jobBlock("0", "FOO=1", "BAR=2"), jobBlock("5", "FOO=1", "BAR=2")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := head + tc.new + tail
			out, changed := mergeCrontab(head+tc.old+tail, "job", tc.new)
			if !changed || out != want {
				t.Fatalf("changed=%v\n got %q\nwant %q", changed, out, want)
			}
			if again, changed := mergeCrontab(out, "job", tc.new); changed || again != out {
				t.Fatalf("second merge must be a no-op: changed=%v out=%q", changed, again)
			}
		})
	}
}

// Negative: when the block's own WithCronEnv lines change, splicing in place
// would apply the new values to every entry below the block, so the block
// is appended at the end instead.
func TestMergeEnvChangeAppendsBlock(t *testing.T) {
	tail := "PATH=/opt/other/bin\n0 * * * * /bin/echo after\n"
	for _, tc := range []struct {
		name     string
		old, new string
	}{
		{"value changed", jobBlock("0", "FOO=1"), jobBlock("0", "FOO=2")},
		{"env added", jobBlock("0"), jobBlock("0", "FOO=2")},
		{"env removed", jobBlock("0", "FOO=1"), jobBlock("0")},
		{"env reordered", jobBlock("0", "A=1", "B=2"), jobBlock("5", "B=2", "A=1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tail + tc.new
			out, changed := mergeCrontab(tc.old+tail, "job", tc.new)
			if !changed || out != want {
				t.Fatalf("changed=%v\n got %q\nwant %q", changed, out, want)
			}
			if again, changed := mergeCrontab(out, "job", tc.new); changed || again != out {
				t.Fatalf("second merge must be a no-op: changed=%v out=%q", changed, again)
			}
		})
	}
}

// An unexpected line inside a hand-edited block counts as environment, so
// the comparison fails safe and the block is appended.
func TestMergeHandEditedBlockAppends(t *testing.T) {
	old := strings.Replace(jobBlock("0"), "0 1 * * * /bin/true\n", "@reboot /bin/true\n0 1 * * * /bin/true\n", 1)
	out, changed := mergeCrontab(old+"A=1\n", "job", jobBlock("5"))
	if want := "A=1\n" + jobBlock("5"); !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}
}

// Blank and comment lines inside an otherwise unchanged block do not touch
// the environment, so they must not force the block to the end.
func TestMergeCommentInBlockStillInPlace(t *testing.T) {
	old := strings.Replace(jobBlock("0", "FOO=1"), "FOO=1\n", "  # keep FOO for the backup\nFOO=1\n\n\t\n", 1)
	tail := "PATH=/opt/other/bin\n0 * * * * /bin/echo after\n"
	out, changed := mergeCrontab(old+tail, "job", jobBlock("5", "FOO=1"))
	if want := jobBlock("5", "FOO=1") + tail; !changed || out != want {
		t.Fatalf("changed=%v\n got %q\nwant %q", changed, out, want)
	}
}

// A CRLF crontab takes the in-place path too and is written back with LF.
func TestMergeCRLFInPlace(t *testing.T) {
	head := "MAILTO=root\n"
	tail := "PATH=/opt/other/bin\n0 * * * * /bin/echo after\n"
	crlf := strings.ReplaceAll(head+jobBlock("0")+tail, "\n", "\r\n")
	out, changed := mergeCrontab(crlf, "job", jobBlock("5"))
	if want := head + jobBlock("5") + tail; !changed || out != want {
		t.Fatalf("changed=%v\n got %q\nwant %q", changed, out, want)
	}

	// An unchanged block in a CRLF crontab is left alone.
	same := strings.ReplaceAll(head+jobBlock("5")+tail, "\n", "\r\n")
	if out, changed := mergeCrontab(same, "job", jobBlock("5")); changed || out != same {
		t.Fatalf("unchanged CRLF block rewritten: changed=%v out=%q", changed, out)
	}
}

func TestMergeInPlaceWithoutTrailingNewline(t *testing.T) {
	out, changed := mergeCrontab(jobBlock("0")+"PATH=/x", "job", jobBlock("5"))
	if want := jobBlock("5") + "PATH=/x\n"; !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}
}

func TestMergeDuplicatesRewrittenAtFirstBlock(t *testing.T) {
	existing := "A=1\n" + jobBlock("0") + "B=2\n" + jobBlock("9") + "C=3\n"
	out, changed := mergeCrontab(existing, "job", jobBlock("5"))
	if want := "A=1\n" + jobBlock("5") + "B=2\nC=3\n"; !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}

	// Duplicates already equal to desired still collapse, in place.
	same := "A=1\n" + jobBlock("5") + "B=2\n" + jobBlock("5")
	out, changed = mergeCrontab(same, "job", jobBlock("5"))
	if want := "A=1\n" + jobBlock("5") + "B=2\n"; !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}
}

func TestMergeUnterminatedMarkers(t *testing.T) {
	// An unclosed BEGIN before a closed block: marker dropped, block
	// rewritten where the closed one stood.
	existing := beginMarker("job") + "\nA=1\n" + jobBlock("0") + "B=2\n"
	out, changed := mergeCrontab(existing, "job", jobBlock("5"))
	if want := "A=1\n" + jobBlock("5") + "B=2\n"; !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}

	// Only an unclosed BEGIN: no closed block existed, so append.
	out, changed = mergeCrontab(beginMarker("job")+"\nPATH=/x\n", "job", jobBlock("5"))
	if want := "PATH=/x\n" + jobBlock("5"); !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}
	if again, changed := mergeCrontab(out, "job", jobBlock("5")); changed || again != out {
		t.Fatalf("healed crontab must be idempotent: changed=%v out=%q", changed, again)
	}
}

func TestMergeRemoveKeepsSurroundingOrder(t *testing.T) {
	existing := "A=1\n" + jobBlock("0") + "B=2\n0 * * * * /bin/echo keep\n"
	out, changed := mergeCrontab(existing, "job", "")
	if want := "A=1\nB=2\n0 * * * * /bin/echo keep\n"; !changed || out != want {
		t.Fatalf("changed=%v got %q want %q", changed, out, want)
	}
	other := strings.ReplaceAll(jobBlock("0"), "Cron[job]", "Cron[other]")
	if out, changed := mergeCrontab(other, "job", ""); changed || out != other {
		t.Fatalf("another job's block was altered: changed=%v out=%q", changed, out)
	}
}

// ensureTwice applies the job twice against a faked crontab holding tab and
// returns the final table and the number of writes.
func ensureTwice(t *testing.T, tab string, opts ...opt.CronOption) (string, int) {
	t.Helper()
	writes := 0
	cr := &runners.CronRunners{
		Read: func(string, ...string) (string, string, int, error) { return tab, "", 0, nil },
		Write: func(stdin string, _ string, _ ...string) (string, string, int, error) {
			writes++
			tab = stdin
			return "", "", 0, nil
		},
	}
	opts = append([]opt.CronOption{opt.WithCronUser(currentCronUser(t)), opt.WithCommand("/bin/true")}, opts...)
	for range 2 {
		if err := EnsureWith(cr, "job", opts...); err != nil {
			t.Fatalf("EnsureWith: %v", err)
		}
	}
	return tab, writes
}

// Adoption and a block change in the same apply: the unmanaged line
// identical to the new schedule is a duplicate of the existing block and is
// dropped, and the changed block stays above the env line below it.
func TestEnsureAdoptionWithBlockChangeKeepsPosition(t *testing.T) {
	tail := "PATH=/opt/other/bin\n0 * * * * /bin/echo after\n"
	tab := jobBlock("0") + tail + "5 1 * * * /bin/true\n"
	got, writes := ensureTwice(t, tab, opt.WithSchedule("5 1 * * *"))
	if want := jobBlock("5") + tail; writes != 1 || got != want {
		t.Fatalf("writes=%d\n got %q\nwant %q", writes, got, want)
	}
}

// WithLegacyCommand removes the old line while the changed block keeps its
// position.
func TestEnsureLegacyWithBlockChangeKeepsPosition(t *testing.T) {
	tail := "PATH=/opt/other/bin\n0 * * * * /bin/echo after\n"
	tab := jobBlock("0") + tail + "3 * * * * /usr/bin/old-job\n"
	got, writes := ensureTwice(t, tab, opt.WithSchedule("5 1 * * *"), opt.WithLegacyCommand("/usr/bin/old-job"))
	if want := jobBlock("5") + tail; writes != 1 || got != want {
		t.Fatalf("writes=%d\n got %q\nwant %q", writes, got, want)
	}
}

// Negative, end to end: a WithCronEnv change moves the block to the end so
// the new value never reaches the entry below the old block.
func TestEnsureEnvChangeDoesNotLeakBelow(t *testing.T) {
	tail := "0 * * * * /bin/echo after\n"
	tab := jobBlock("0", "FOO=1") + tail
	got, writes := ensureTwice(t, tab, opt.WithSchedule("0 1 * * *"), opt.WithCronEnv("FOO=2"))
	if want := tail + jobBlock("0", "FOO=2"); writes != 1 || got != want {
		t.Fatalf("writes=%d\n got %q\nwant %q", writes, got, want)
	}
}
