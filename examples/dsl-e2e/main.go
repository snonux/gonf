// Command dsl-e2e declares disposable resources for live DSL verification.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
	"github.com/snonux/gonf/resource"
)

type lodge struct{}
type postcards struct{}
type account struct{ RequiresRoot }

func main() {
	goos := os.Getenv("GONF_E2E_GOOS")
	if goos == "" {
		goos = runtime.GOOS
	}
	resource.SetDryRun(os.Getenv("GONF_E2E_DRY_RUN") == "1")
	RegisterGroup([]any{lodge{}, postcards{}}, WithPrefix("fixture_"))
	RegisterMethods(account{}, WithPrefix("fixture_"))
	Task("excluded", "Never run on the selected OS", func() {
		File(filepath.Join(destination(), "must-not-exist"), WithContent("guard failed"))
	}, WhenNotOS(goos))
	if action := os.Getenv("GONF_E2E_DELIVERY"); action != "" {
		deliver(action)
		return
	}
	cli.Main()
}

// Base installs the lodge and ordered shell variables.
func (lodge) Base() {
	Dir(destination(), WithMode(0o755))
	File(filepath.Join(destination(), "lodge.conf"),
		WithShellVars("GONFY_NAME", "Gonfy the beaver", "GONFY_ENABLED", "YES"),
		WithMode(0o644))
}

// OptsCopy orders source deployment after the lodge.
func (postcards) OptsCopy() TaskOptions { return TaskOptions{Needs("fixture_base")} }

// Copy packages required and optional controller sources with exclusions.
func (postcards) Copy() {
	src := os.Getenv("GONF_E2E_SOURCE")
	required := RequireControllerFile(filepath.Join(src, "welcome.txt"),
		"Gonfy's welcome postcard", "create the fixture source first")
	if required != "" {
		InstallFile(filepath.Join(destination(), "welcome.txt"), required, WithMode(0o644))
	}
	if OptionalControllerDir(filepath.Join(src, "postcards"), "Gonfy's optional postcards") {
		Dir(filepath.Join(destination(), "postcards"),
			WithSource(filepath.Join(src, "postcards")), WithExclude("private", "*.tmp"),
			WithPrune, WithMode(0o755), WithFileMode(0o644))
	}
}

// Shell converges only the disposable account's shell.
func (account) Shell() {
	User("gonfy_e2e", WithShell("/bin/sh"), WithManageShell)
}

// OptsSysctl orders persistent settings after their directory.
func (account) OptsSysctl() TaskOptions { return TaskOptions{Needs("fixture_base")} }

// Sysctl persists and probes the caller-selected live value.
func (account) Sysctl() {
	Sysctls(filepath.Join(destination(), "sysctl.conf"),
		List(os.Getenv("GONF_E2E_SYSCTL"), os.Getenv("GONF_E2E_VALUE")), RootOwned)
}

// Enable manages only the disposable service's boot state.
func (account) Enable() {
	Service("gonfy_e2e", WithEnableOnly)
}

func destination() string {
	if path := os.Getenv("GONF_E2E_DESTINATION"); path != "" {
		return path
	}
	return "/tmp/gonfy-dsl-e2e-l93"
}

func deliver(action string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	target := PushTarget{
		Host: os.Getenv("GONF_E2E_HOST"), User: os.Getenv("GONF_E2E_USER"), Port: 22,
		GonfPath: os.Getenv("GONF_E2E_RECEIVER"),
	}
	switch os.Getenv("GONF_E2E_PRIVILEGE") {
	case "doas":
		target.Privilege = PrivilegeDoas
	case "sudo":
		target.Privilege = PrivilegeSudo
	}
	tasks := os.Args[1:]
	var err error
	switch action {
	case "preview":
		err = PreviewToContext(ctx, target, "dsl-e2e", tasks...)
	case "push":
		err = PushToContext(ctx, target, "dsl-e2e", tasks...)
	default:
		err = fmt.Errorf("unknown delivery %q", action)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
