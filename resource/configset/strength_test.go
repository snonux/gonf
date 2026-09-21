package configset

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/snonux/gonf/internal/validator"
	"github.com/snonux/gonf/plan"
	"github.com/snonux/gonf/resource"
	"github.com/snonux/gonf/resource/file"
	opt "github.com/snonux/gonf/resource/options"
)

// A failed attribute repair of an unchanged member aborts before any live
// rename (so no change is stranded unsignalled), and the next apply
// publishes and signals the change.
func TestAttributeRepairFailureThenReapplySignals(t *testing.T) {
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	orig := applyAttributes
	t.Cleanup(func() { applyAttributes = orig })
	applyAttributes = func(*file.Target) error { return errors.New("injected chown failure") }
	if err := f.apply("root: paul\npostmaster: root\n"); err == nil || !strings.Contains(err.Error(), "injected chown failure") {
		t.Fatalf("apply error = %v, want the repair failure", err)
	}
	if got := readFile(t, f.aliasesPath()); got != "root: paul\n" {
		t.Fatalf("aliases = %q: a failed attribute repair must come before any live rename", got)
	}
	applyAttributes = orig
	if err := f.apply("root: paul\npostmaster: root\n"); err != nil {
		t.Fatal(err)
	}
	if !outcomeOf(t, "aliases") {
		t.Fatal("the re-apply must signal the aliases change")
	}
}

// A publication also repairs the mode of an unchanged member, without
// reporting that member as changed.
func TestChangedSetRepairsModeOfUnchangedMember(t *testing.T) {
	f := newFixture(t)
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.confPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.apply("root: paul\npostmaster: root\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.confPath())
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("smtpd.conf mode = %v (%v), want repaired 0640", info.Mode().Perm(), err)
	}
	if !outcomeOf(t, "aliases") || outcomeOf(t, "smtpd.conf") {
		t.Fatal("only aliases may report a change; the mode repair is silent")
	}
}

// The copy fallback carries the original owner and mode onto both the backup
// copy and the restored file, chown before chmod each time (a chown may clear
// setuid/setgid bits, so the chmod must be last).
func TestRollbackCopyCarriesOwnerAndModeInOrder(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o604); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.aliasesPath(), 0o604); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.aliasesPath())
	if err != nil {
		t.Fatal(err)
	}
	st := info.Sys().(*syscall.Stat_t)
	var calls []string
	origLink, origChown, origChmod := linkBackup, chownFile, chmodFile
	t.Cleanup(func() { linkBackup, chownFile, chmodFile = origLink, origChown, origChmod })
	linkBackup = func(int, string, string) error { return errors.New("EXDEV (injected)") }
	chownFile = func(fh *os.File, uid, gid int) error {
		calls = append(calls, "chown "+strconv.Itoa(uid)+":"+strconv.Itoa(gid))
		return origChown(fh, uid, gid)
	}
	chmodFile = func(fh *os.File, mode os.FileMode) error {
		calls = append(calls, "chmod "+mode.String())
		return origChmod(fh, mode)
	}
	failSecondWrite(t, nil)
	if err := f.apply("root: paul\n"); err == nil || !strings.Contains(err.Error(), "rolled back 2 member(s)") {
		t.Fatalf("apply error = %v, want a completed rollback", err)
	}
	owner := "chown " + strconv.Itoa(int(st.Uid)) + ":" + strconv.Itoa(int(st.Gid))
	mode := "chmod " + os.FileMode(0o604).String()
	want := []string{owner, mode, owner, mode} // backup copy, then restore
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Fatalf("attribute calls = %v, want %v", calls, want)
	}
	if got := readFile(t, f.aliasesPath()); got != "old aliases\n" {
		t.Fatalf("aliases = %q, want restored", got)
	}
}

// Every validator runs: a set whose first validator passes and second fails
// publishes nothing.
func TestSecondValidatorFailureStopsPublication(t *testing.T) {
	f := newFixture(t)
	opts := append(f.options("root: paul\n"),
		opt.WithSetValidation("sh", []string{"-c", "echo second validator rejects >&2; exit 1"}))
	resource.ResetReport()
	err := Ensure("mail", opts...)
	if err == nil || !strings.Contains(err.Error(), "second validator rejects") {
		t.Fatalf("apply error = %v, want the second validator's failure", err)
	}
	if len(f.validatorRuns()) != 1 {
		t.Fatal("the first validator must have run and passed")
	}
	mustNotExist(t, f.aliasesPath())
}

// A root chroot or a staging directory computed as "/" must be accepted:
// within("/", p) holds for every absolute p.
func TestRootChrootAndRootStagingParent(t *testing.T) {
	s := validSpec()
	s.chroot = "/"
	s.members[0].content = []byte("include " + opt.MemberChrootPath("b"))
	if err := s.validate(); err != nil {
		t.Fatalf("chroot / rejected: %v", err)
	}
	got, err := render(s.members[0].content, s.liveResolver())
	if err != nil || string(got) != "include /etc/app/keys/b.conf" {
		t.Fatalf("chroot-relative path under / = %q (%v)", got, err)
	}
	s = validSpec()
	s.members[1].path = "/var/b.conf"
	if parent := s.stagingParent(); parent != "/" {
		t.Fatalf("staging parent of /etc/app/a.conf and /var/b.conf = %q, want /", parent)
	}
	if err := s.validate(); err != nil {
		t.Fatalf("members under / rejected: %v", err)
	}
}

// Members up to exactly plan.MaxInlineContent bytes are accepted; one more
// byte is refused at record time.
func TestInlineContentBoundary(t *testing.T) {
	draft := func(n int) resource.PlanDraft {
		return resource.PlanDraft{
			Kind: string(plan.KindConfigSet), ID: "ConfigSet[big]", Name: "big",
			ConfigMembers: []resource.PlanConfigMember{{Key: "big", Path: "/etc/big.conf", Content: make([]byte, n), Mode: "0640"}},
			Validators:    []resource.PlanArgv{{Bin: "true"}},
		}
	}
	if _, err := (setHandler{}).ToOp(draft(plan.MaxInlineContent)); err != nil {
		t.Fatalf("exactly the inline limit must be accepted: %v", err)
	}
	if _, err := (setHandler{}).ToOp(draft(plan.MaxInlineContent + 1)); err == nil || !strings.Contains(err.Error(), "inline limit") {
		t.Fatalf("one byte over the inline limit: err = %v, want refusal", err)
	}
}

// Set validators run through the shared runner: its capped, sanitized output
// ends up in the error (NUL bytes become '?', a huge output is truncated to
// validator.OutputLimit),
// and the working directory is the staged candidate mirror.
func TestSetValidatorUsesSharedBoundedRunner(t *testing.T) {
	f := newFixture(t)
	opts := append(f.options("root: paul\n"),
		opt.WithSetValidation("sh", []string{"-c", "head -c 20000 /dev/zero; exit 3"}))
	resource.ResetReport()
	err := Ensure("mail", opts...)
	if err == nil || !strings.Contains(err.Error(), "validator output: ????") || !strings.Contains(err.Error(), "[output truncated, 20000 bytes in total]") {
		t.Fatalf("apply error = %v, want the capped shared-runner output", err)
	}
	if len(err.Error()) > validator.OutputLimit+512 {
		t.Fatalf("error is %d bytes, want it bounded near validator.OutputLimit", len(err.Error()))
	}

	var dirs []string
	orig := runValidator
	t.Cleanup(func() { runValidator = orig })
	runValidator = func(dir, bin string, args []string) error {
		dirs = append(dirs, dir)
		return orig(dir, bin, args)
	}
	if err := f.apply("root: paul\n"); err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || !strings.HasSuffix(dirs[0], "/candidates") || !strings.Contains(dirs[0], stagePrefix+"mail+") {
		t.Fatalf("validator working directories = %v, want the staged candidate mirror", dirs)
	}
}

// When creating the second member's marker fails, only the first member
// (already replaced) is rolled back.
func TestMarkerFailureRollsBackOnlyReplacedMembers(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.aliasesPath(), []byte("old aliases\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := syncDirFD
	t.Cleanup(func() { syncDirFD = orig })
	calls := 0
	syncDirFD = func(fd int, dir string) error {
		calls++
		if calls == 2 { // the second marker creation
			return errors.New("injected fsync failure")
		}
		return orig(fd, dir)
	}
	err := f.apply("root: paul\n")
	if err == nil || !strings.Contains(err.Error(), "rolled back 1 member(s)") || !strings.Contains(err.Error(), "smtpd.conf") {
		t.Fatalf("apply error = %v, want smtpd.conf's marker failure and a rollback of 1 member", err)
	}
	if got := readFile(t, f.aliasesPath()); got != "old aliases\n" {
		t.Fatalf("aliases = %q, want restored", got)
	}
	mustNotExist(t, f.confPath())
	mustNotExist(t, filepath.Join(f.etc, "mail", markerName("mail", f.memberOf("smtpd.conf"))))
}
