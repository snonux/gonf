package foostore

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The fake foostore is this test binary re-executed (the TestHelperProcess
// pattern): newFake points a Provider at os.Args[0] with a -test.run prefix,
// and TestHelperProcess plays foostore according to FAKE_MODE. It never
// touches a real store or credential.

// fakeUsage is the start of the usage text foostore's contract v1 prints for
// `read --help` (copied from foostore's readUsage).
const fakeUsage = `usage: foostore read [--field NAME] [--kdbx-path PATH] [--backend keepass] [--timeout DURATION] [--exact] [--raw] [--non-interactive] REFERENCE

Exit codes: 0 ok; 1 unexpected failure or timeout; 2 usage;
4 not found (the only suppressible code); 5 ambiguous identity;
6 locked or unusable credentials; 7 corrupt store; 8 store I/O.`

// leak is the marker every negative fake prints on stdout and stderr; no
// error may ever contain it.
const leak = "TOPSECRET-hunter2"

// modeExits are the fakes that print the leak marker and exit with a code.
var modeExits = map[string]int{
	"exit1": 1, "usage": 2, "garbage": 3, "notfound": 4, "ambiguous": 5,
	"locked": 6, "corrupt": 7, "io": 8,
}

// TestHelperProcess is the fake foostore; it does nothing in a normal run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GONF_FAKE_FOOSTORE") != "1" {
		t.Skip("helper process only")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	os.Exit(fakeFoostore(args))
}

// fakeFoostore implements the fake's behaviour and returns its exit code.
func fakeFoostore(args []string) int {
	mode := os.Getenv("FAKE_MODE")
	if len(args) == 2 && args[0] == "read" && args[1] == "--help" {
		return fakeProbe(mode)
	}
	fakeLog("read")
	if want := os.Getenv("FAKE_EXPECT"); want != "" && want != strings.Join(args, "\x1f") {
		fmt.Fprintf(os.Stderr, "argv mismatch: %q\n", args)
		return 97
	}
	if code, ok := modeExits[mode]; ok {
		fmt.Print(leak)
		fmt.Fprint(os.Stderr, "diagnostic "+leak)
		return code
	}
	return fakeSpecial(mode)
}

// fakeSpecial covers the modes that do more than print and exit.
func fakeSpecial(mode string) int {
	switch mode {
	case "ok":
		if msg := interactiveEscape(); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			return 98
		}
		return writeValue()
	case "passfd":
		return fakePassFD()
	case "prompt":
		return fakePrompt()
	case "big":
		fmt.Print(strings.Repeat(leak, 100))
		return 0
	case "hang":
		return fakeHang()
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
		time.Sleep(time.Minute)
	}
	return 99
}

// fakeProbe answers `read --help`: the contract usage, or what an older or
// wrong binary would print.
func fakeProbe(mode string) int {
	fakeLog("probe")
	switch mode {
	case "old":
		fmt.Println("Vault/" + leak) // an interactive search result
		return 0
	case "probefail":
		fmt.Fprintln(os.Stderr, leak)
		return 3
	}
	fmt.Println(fakeUsage)
	return 0
}

// interactiveEscape reports any way the child could still interact: a
// controlling terminal, readable stdin, or an inherited interactive
// override in its environment.
func interactiveEscape() string {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		_ = f.Close()
		return "controlling terminal reachable"
	}
	if n, _ := io.Copy(io.Discard, os.Stdin); n != 0 {
		return "stdin carried data"
	}
	for _, kv := range os.Environ() {
		for _, bad := range []string{"FOOSTORE_SHELL=", "PIN=", "FOOSTORE_READ_PASSPHRASE_FD="} {
			if strings.HasPrefix(kv, bad) {
				return "inherited " + bad
			}
		}
	}
	return ""
}

// writeValue prints FAKE_VALUE_B64 decoded, byte for byte.
func writeValue() int {
	v, err := base64.StdEncoding.DecodeString(os.Getenv("FAKE_VALUE_B64"))
	if err != nil {
		return 96
	}
	_, _ = os.Stdout.Write(v)
	return 0
}

// fakePassFD reads the passphrase from the inherited descriptor like
// foostore does and succeeds only when it matches FAKE_PASS.
func fakePassFD() int {
	fd, err := strconv.Atoi(os.Getenv("FOOSTORE_READ_PASSPHRASE_FD"))
	if err != nil {
		return 95
	}
	pass, err := io.ReadAll(os.NewFile(uintptr(fd), "passphrase"))
	if err != nil || string(pass) != os.Getenv("FAKE_PASS") {
		fmt.Fprint(os.Stderr, "wrong passphrase")
		return 6
	}
	return writeValue()
}

// fakePrompt is a foostore that would fall back to asking for the
// passphrase: it succeeds only if a terminal or stdin could answer.
func fakePrompt() int {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		_ = f.Close()
		fmt.Print("INTERACTIVE")
		return 0
	}
	if b, _ := io.ReadAll(os.Stdin); len(b) > 0 {
		fmt.Print("INTERACTIVE")
		return 0
	}
	fmt.Fprint(os.Stderr, "no passphrase source")
	return 6
}

// fakeHang starts a grandchild holding the output pipes, records both pids
// in FAKE_PIDS and blocks, so tests can check the process group is killed.
func fakeHang() int {
	// The child environment has no PATH, so the test passes sleep's path.
	sleeper := exec.Command(os.Getenv("FAKE_SLEEP"), "3600")
	sleeper.Stdout, sleeper.Stderr = os.Stdout, os.Stderr
	if err := sleeper.Start(); err != nil {
		return 94
	}
	pids := fmt.Sprintf("%d %d", os.Getpid(), sleeper.Process.Pid)
	if err := os.WriteFile(os.Getenv("FAKE_PIDS"), []byte(pids), 0o600); err != nil {
		return 93
	}
	fmt.Print(leak)
	time.Sleep(time.Hour)
	return 0
}

// fakeLog appends one event to FAKE_LOG so tests can count invocations.
func fakeLog(event string) {
	path := os.Getenv("FAKE_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintln(f, event)
}
