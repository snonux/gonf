// Command gonf manages persistent and live sysctls (tutorial chapter 7).
package main

import (
	"os"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func settings() (string, string) {
	value := os.Getenv("SYSCTL_SWAPPINESS")
	if value == "" {
		value = "60"
	}
	return DestHome("gonf-tutorial/lodge-sysctl.conf"), value
}

func separate() {
	path, value := settings()
	conf := File(path, WithKeyedLine("vm.swappiness=", "vm.swappiness="+value), WithMode(0o644))
	Command("sysctl", List("vm.swappiness="+value), WithName("sysctl.vm.swappiness"), DependsOn(conf),
		Unless("sysctl", List("-n", "vm.swappiness"), ExpectStdout(value)))
}

func combined() {
	path, value := settings()
	Sysctls(path, List("vm.swappiness", value), WithMode(0o644))
}

func main() {
	Task("separate", "Manage Gonfy's setting with File and Command", separate, WhenLinux())
	Task("combined", "Manage the same setting with Sysctls", combined, WhenLinux())
	cli.Main()
}
