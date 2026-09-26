// Command gonf decides where tasks and resources apply (tutorial
// chapter 9).
package main

import (
	"os"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	dir := DestHome("gonf-tutorial/guards")
	note := func(name string) func() {
		return func() { File(dir+"/"+name, WithContent("Gonfy was here: "+name+"\n"), WithMode(0o644)) }
	}

	// Every task below Needs this one, so the directory exists first.
	Task("dir", "Create ~/gonf-tutorial/guards", func() { Dir(dir, WithMode(0o755)) })
	need := Needs("dir")

	// Task-level guards that travel in the plan: the destination decides.
	Task("linux", "Only on Linux", note("linux"), WhenLinux(), need)
	Task("bsd", "Only on the BSDs", note("bsd"), WhenBSD(), need)
	Task("fedora", "Only on Fedora", note("fedora"), WhenProfile("fedora"), need)
	Task("laptop", "Only on hosts named *laptop*", note("laptop"), WhenHostnameContains("laptop"), need)

	// An opaque predicate is Go code: it runs on the controller only.
	Task("big", "Only when the controller has 4+ CPUs", note("big"),
		When(func(Facts) bool { return cpus() >= 4 }), need)

	// Guards inside a body wrap only some resources.
	Task("mixed", "Body-level guards", func() {
		WhenHostname(List("vm", "laptop"), func() {
			File(dir+"/hostname-match", WithContent("vm or laptop\n"), WithMode(0o644))
		})
		WhenPathExists("/etc/debian_version", func() {
			File(dir+"/debian-family", WithContent("yes\n"), WithMode(0o644))
		})
	}, need)

	Aggregate("everything", "Every guarded task", "^(linux|bsd|fedora|laptop|big|mixed)$")
	cli.Main()
}

// cpus counts the controller's CPUs.
func cpus() int {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return 1
	}
	n := 0
	for _, line := range splitLines(string(b)) {
		if len(line) >= 9 && line[:9] == "processor" {
			n++
		}
	}
	return n
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
