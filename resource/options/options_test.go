package options

import (
	"testing"

	"github.com/snonux/gonf/resource"
)

// These helpers intentionally accept the concrete family interfaces. The
// table therefore fails to compile when an option is accidentally returned
// with the wrong family marker, which is the contract resource constructors
// rely on for compile-time mismatch detection.
func acceptFile(FileOption)                 {}
func acceptDir(DirOption)                   {}
func acceptLink(LinkOption)                 {}
func acceptPackage(PackageOption)           {}
func acceptService(ServiceOption)           {}
func acceptCron(CronOption)                 {}
func acceptTimer(TimerOption)               {}
func acceptSystemdTimer(SystemdTimerOption) {}
func acceptDaemonReload(DaemonReloadOption) {}
func acceptCommand(CommandOption)           {}
func acceptLocalUser(LocalUserOption)       {}

func TestEveryExportedOptionHasAResourceFamily(t *testing.T) {
	tests := []struct {
		name   string
		accept func()
	}{
		{"DependsOn", func() { acceptFile(DependsOn()) }},
		{"WithOwner", func() { acceptFile(WithOwner("user")) }},
		{"WithGroup", func() { acceptDir(WithGroup("group")) }},
		{"WithUserGroup", func() { acceptLocalUser(WithUserGroup("wheel")) }},
		{"WithPrimaryGroup", func() { acceptLocalUser(WithPrimaryGroup("svc")) }},
		{"WithSupplementaryGroups", func() { acceptLocalUser(WithSupplementaryGroups("audio", "wheel")) }},
		{"WithLoginClass", func() { acceptLocalUser(WithLoginClass("daemon")) }},
		{"WithHome", func() { acceptLocalUser(WithHome("/var/lib/svc")) }},
		{"WithCreateHome", func() { acceptLocalUser(WithCreateHome) }},
		{"WithShell", func() { acceptLocalUser(WithShell("/sbin/nologin")) }},
		{"WithSystem", func() { acceptLocalUser(WithSystem) }},
		{"WithMode", func() { acceptFile(WithMode(0o644)) }},
		{"WithSource", func() { acceptDir(WithSource("source")) }},
		{"WithSourceGlob", func() { acceptDir(WithSourceGlob("*.conf")) }},
		{"WithParam", func() { acceptFile(WithParam("param")) }},
		{"WithTemplate", func() { acceptFile(WithTemplate) }},
		{"WithTemplateData", func() { acceptFile(WithTemplateData(map[string]any{})) }},
		{"WithValidation", func() { acceptFile(WithValidation("validator", []string{CandidatePath})) }},
		{"WithSourceBase", func() { acceptDir(WithSourceBase("source")) }},
		{"WithContent", func() { acceptFile(WithContent("content")) }},
		{"WithLines", func() { acceptFile(WithLines("one", "two")) }},
		{"WithoutLines", func() { acceptFile(WithoutLines("one", "two")) }},
		{"WithLine", func() { acceptFile(WithLine("line")) }},
		{"WithoutLine", func() { acceptFile(WithoutLine("line")) }},
		{"WithFileMode", func() { acceptDir(WithFileMode(0o640)) }},
		{"WithPrune", func() { acceptDir(WithPrune) }},
		{"IsAbsent", func() { acceptFile(IsAbsent) }},
		{"IsLatest", func() { acceptPackage(IsLatest) }},
		{"WithRestart", func() { acceptService(WithRestart) }},
		{"WithReload", func() { acceptService(WithReload) }},
		{"WithUser", func() { acceptDaemonReload(WithUser) }},
		{"WithElevate", func() { acceptCommand(WithElevate) }},
		{"WithEnableOnly", func() { acceptTimer(WithEnableOnly) }},
		{"IfChanged", func() { acceptDaemonReload(IfChanged) }},
		{"WithWatch", func() { acceptDaemonReload(WithWatch("id")) }},
		{"OnChange", func() { acceptCommand(OnChange(resource.Resource{Type: "File", Name: "unit"})) }},
		{"WatchChanges", func() { acceptTimer(WatchChanges("File[unit]")) }},
		{"WithCronUser", func() { acceptCron(WithCronUser("root")) }},
		{"WithLegacyCommand", func() { acceptCron(WithLegacyCommand("/usr/local/bin/old")) }},
		{"WithCommand", func() { acceptSystemdTimer(WithCommand("true")) }},
		{"WithMinute", func() { acceptCron(WithMinute("*")) }},
		{"WithHour", func() { acceptCron(WithHour("*")) }},
		{"WithMonthday", func() { acceptCron(WithMonthday("*")) }},
		{"WithMonth", func() { acceptCron(WithMonth("*")) }},
		{"WithWeekday", func() { acceptCron(WithWeekday("*")) }},
		{"WithCronEnv", func() { acceptCron(WithCronEnv("KEY=value")) }},
		{"WithOnCalendar", func() { acceptSystemdTimer(WithOnCalendar("daily")) }},
		{"WithOnBootSec", func() { acceptSystemdTimer(WithOnBootSec("1min")) }},
		{"WithPersistent", func() { acceptSystemdTimer(WithPersistent) }},
		{"WithDescription", func() { acceptSystemdTimer(WithDescription("description")) }},
		{"WithServiceDescription", func() { acceptSystemdTimer(WithServiceDescription("description")) }},
		{"WithAfter", func() { acceptSystemdTimer(WithAfter("network.target")) }},
		{"WithWants", func() { acceptSystemdTimer(WithWants("network.target")) }},
		{"WithSymlink", func() { acceptLink(WithSymlink("target")) }},
		{"WithHardlink", func() { acceptLink(WithHardlink("target")) }},
		{"WithName", func() {
			acceptFile(WithName("name"))
			acceptCommand(WithName("name"))
		}},
		{"WithDir", func() { acceptCommand(WithDir("/work")) }},
		{"WithEnv", func() {
			acceptCommand(WithEnv(map[string]string{"KEY": "value"}))
			acceptPackage(WithEnv(map[string]string{"KEY": "value"}))
		}},
		{"Creates", func() { acceptCommand(Creates("/marker")) }},
		{"Unless", func() { acceptCommand(Unless("test", nil)) }},
		{"OnlyIf", func() { acceptCommand(OnlyIf("test", nil)) }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { test.accept() })
	}
}

func TestSharedOptionsHaveCompleteFamilyMatrix(t *testing.T) {
	acceptAll := func(option AllResourceOption) {
		acceptFile(option)
		acceptDir(option)
		acceptLink(option)
		acceptPackage(option)
		acceptService(option)
		acceptCron(option)
		acceptTimer(option)
		acceptSystemdTimer(option)
		acceptDaemonReload(option)
		acceptCommand(option)
	}
	acceptFileDir := func(option FileDirOption) {
		acceptFile(option)
		acceptDir(option)
	}
	acceptAbsent := func(option AbsentOption) {
		acceptFile(option)
		acceptDir(option)
		acceptLink(option)
		acceptPackage(option)
		acceptService(option)
		acceptCron(option)
		acceptTimer(option)
		acceptSystemdTimer(option)
	}
	acceptServiceTimer := func(option ServiceTimerOption) {
		acceptService(option)
		acceptTimer(option)
		acceptSystemdTimer(option)
	}
	acceptUser := func(option UserOption) {
		acceptService(option)
		acceptTimer(option)
		acceptSystemdTimer(option)
		acceptDaemonReload(option)
	}
	acceptEnableOnly := func(option EnableOnlyOption) {
		acceptTimer(option)
		acceptSystemdTimer(option)
	}
	acceptCronSystemdTimer := func(option CronSystemdTimerOption) {
		acceptCron(option)
		acceptSystemdTimer(option)
	}

	acceptAll(DependsOn())
	acceptLocalUser(DependsOn())
	acceptFileDir(WithOwner("user"))
	acceptFileDir(WithGroup("group"))
	acceptLocalUser(WithUserGroup("wheel"))
	acceptLocalUser(WithPrimaryGroup("svc"))
	acceptLocalUser(WithSupplementaryGroups("audio"))
	acceptLocalUser(WithLoginClass("daemon"))
	acceptLocalUser(WithHome("/var/lib/svc"))
	acceptLocalUser(WithCreateHome)
	acceptLocalUser(WithShell("/sbin/nologin"))
	acceptLocalUser(WithSystem)
	acceptFileDir(WithMode(0o644))
	acceptFileDir(WithSource("source"))
	acceptDir(WithSourceGlob("*.conf"))
	acceptFile(WithParam("param"))
	acceptFile(WithTemplate)
	acceptFile(WithTemplateData(map[string]any{}))
	acceptFile(WithValidation("validator", []string{CandidatePath}))
	acceptDir(WithSourceBase("source"))
	acceptFile(WithContent("content"))
	acceptFile(WithLine("line"))
	acceptFile(WithoutLine("line"))
	acceptDir(WithFileMode(0o640))
	acceptDir(WithPrune)
	acceptAbsent(IsAbsent)
	acceptPackage(IsLatest)
	acceptServiceTimer(WithRestart)
	acceptService(WithReload)
	acceptUser(WithUser)
	acceptCommand(WithElevate)
	acceptEnableOnly(WithEnableOnly)
	acceptDaemonReload(IfChanged)
	acceptDaemonReload(WithWatch("id"))
	acceptChangeGate := func(option ChangeGateOption) {
		acceptCommand(option)
		acceptService(option)
		acceptTimer(option)
		acceptDaemonReload(option)
	}
	acceptChangeGate(OnChange(resource.Resource{Type: "File", Name: "unit"}))
	acceptChangeGate(WatchChanges("File[unit]"))
	acceptCron(WithCronUser("root"))
	acceptCron(WithLegacyCommand("/usr/local/bin/old"))
	acceptCronSystemdTimer(WithCommand("true"))
	acceptCron(WithMinute("*"))
	acceptCron(WithHour("*"))
	acceptCron(WithMonthday("*"))
	acceptCron(WithMonth("*"))
	acceptCron(WithWeekday("*"))
	acceptCron(WithCronEnv("KEY=value"))
	acceptSystemdTimer(WithOnCalendar("daily"))
	acceptSystemdTimer(WithOnBootSec("1min"))
	acceptSystemdTimer(WithPersistent)
	acceptSystemdTimer(WithDescription("description"))
	acceptSystemdTimer(WithServiceDescription("description"))
	acceptSystemdTimer(WithAfter("network.target"))
	acceptSystemdTimer(WithWants("network.target"))
	acceptLink(WithSymlink("target"))
	acceptLink(WithHardlink("target"))
	acceptCommand(WithName("name"))
	acceptCommand(WithDir("/work"))
	acceptCommand(WithEnv(map[string]string{"KEY": "value"}))
	acceptPackage(WithEnv(map[string]string{"KEY": "value"}))
	acceptCommand(Creates("/marker"))
	acceptCommand(Unless("test", nil))
	acceptCommand(OnlyIf("test", nil))
}

func TestLegacyOptionAdapters(t *testing.T) {
	tests := []struct {
		name string
		got  int
		want int
	}{
		{"file", len(ToFileOptions(WithContent("x"))), 1},
		{"directory", len(ToDirOptions(WithMode(0o755))), 1},
		{"link", len(ToLinkOptions(WithSymlink("target"))), 1},
		{"package", len(ToPackageOptions(IsLatest, WithEnv(map[string]string{"PKG_PATH": "repo"}))), 2},
		{"service", len(ToServiceOptions(WithReload)), 1},
		{"cron", len(ToCronOptions(WithMinute("*"))), 1},
		{"timer", len(ToTimerOptions(WithRestart)), 1},
		{"systemd timer", len(ToSystemdTimerOptions(WithOnCalendar("daily"))), 1},
		{"daemon reload", len(ToDaemonReloadOptions(IfChanged)), 1},
		{"command", len(ToCommandOptions(WithName("name"))), 1},
		{"local user", len(ToLocalUserOptions(WithLoginClass("daemon"))), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("adapter returned %d options, want %d", test.got, test.want)
			}
		})
	}

	called := false
	legacy := Option(func(any) { called = true })
	ToFileOptions(legacy)[0].Apply(nil)
	if !called {
		t.Fatal("legacy custom option was not applied by ToFileOptions")
	}
}

func TestInvalidOptionResourcePairDoesNotCompile(t *testing.T) {
	const source = `package invalid

import (
	"github.com/snonux/gonf/resource/file"
	"github.com/snonux/gonf/resource/options"
)

func invalid() {
	file.Present("/tmp/file", options.WithCommand("true"))
}
`
	errs := typeErrors(t, source) // sensitive_test.go
	if len(errs) == 0 {
		t.Fatal("invalid file option was accepted by the compiler")
	}
	for _, loadErr := range errs {
		if loadErr.Pos != "" && loadErr.Msg != "" {
			return
		}
	}
	t.Fatalf("unexpected package errors: %v", errs)
}
