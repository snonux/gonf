package options

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/snonux/gonf/resource"
)

// family is one resource-family option interface, probed at run time: as
// reports whether an option value implements the family and, if so, returns
// the Apply method reached through that interface.
type family struct {
	name string
	as   func(option any) (apply func(any), ok bool)
}

// familyOf builds the runtime probe for family interface T.
func familyOf[T interface{ Apply(any) }](name string) family {
	return family{name: name, as: func(option any) (func(any), bool) {
		typed, ok := option.(T)
		if !ok {
			return nil, false
		}
		return typed.Apply, true
	}}
}

// allFamilies lists every resource-family interface a constructor accepts.
var allFamilies = []family{
	familyOf[FileOption]("File"),
	familyOf[DirOption]("Dir"),
	familyOf[LinkOption]("Link"),
	familyOf[PackageOption]("Package"),
	familyOf[ServiceOption]("Service"),
	familyOf[CronOption]("Cron"),
	familyOf[TimerOption]("Timer"),
	familyOf[SystemdTimerOption]("SystemdTimer"),
	familyOf[DaemonReloadOption]("DaemonReload"),
	familyOf[CommandOption]("Command"),
	familyOf[LocalUserOption]("LocalUser"),
	familyOf[ConfigSetOption]("ConfigSet"),
}

// Family sets shared by several options; named after the unexported option
// type that carries them.
var (
	famAll          = []string{"File", "Dir", "Link", "Package", "Service", "Cron", "Timer", "SystemdTimer", "DaemonReload", "Command", "LocalUser"}
	famFileDir      = []string{"File", "Dir"}
	famGroup        = []string{"File", "Dir", "LocalUser"}
	famAbsent       = []string{"File", "Dir", "Link", "Package", "Service", "Cron", "Timer", "SystemdTimer"}
	famServiceTimer = []string{"Service", "Timer", "SystemdTimer"}
	famUser         = []string{"Service", "Timer", "SystemdTimer", "DaemonReload"}
	famChangeGate   = []string{"Service", "Timer", "DaemonReload", "Command"}
	famEnableOnly   = []string{"Timer", "SystemdTimer"}
	famCronTimer    = []string{"Cron", "SystemdTimer"}
	// famSensitive lists the payload-carrying kinds WithSensitive accepts;
	// Link, Service, Timer, DaemonReload and LocalUser carry no payload.
	famSensitive = []string{"File", "Dir", "Package", "Cron", "SystemdTimer", "Command", "ConfigSet"}
	// famDependsOn is famAll plus ConfigSet: DependsOn's allResourceOption
	// also implements the config-set family (resource/options/configset.go),
	// which AllResourceOption itself does not list.
	famDependsOn = append(slices.Clone(famAll), "ConfigSet")
)

// one builds the expected single-setter call list.
func one(method string, value any) []setterCall {
	return []setterCall{{method: method, value: value}}
}

// optionCase pins one option: the families that accept it and the setter
// calls it makes on a fully capable resource.
type optionCase struct {
	name     string
	option   any
	families []string
	want     []setterCall
}

var fileA = resource.Resource{Type: "File", Name: "a"}

// optionCases covers every exported option. Values are distinct per option
// so a closure that forwards the wrong argument is caught.
var optionCases = []optionCase{
	{"DependsOn", DependsOn(fileA), famDependsOn, one("AddDependency", "File[a]")},
	{"WithOwner", WithOwner("paul"), famFileDir, one("SetOwner", "paul")},
	{"WithGroup", WithGroup("wheel"), famGroup, one("SetGroup", "wheel")},
	{"WithHome", WithHome("/var/lib/svc"), []string{"LocalUser"}, one("SetHome", "/var/lib/svc")},
	{"WithCreateHome", WithCreateHome, []string{"LocalUser"}, one("SetCreateHome", nil)},
	{"WithManageHome", WithManageHome, []string{"LocalUser"}, one("SetManageHome", nil)},
	{"WithShell", WithShell("/sbin/nologin"), []string{"LocalUser"}, one("SetShell", "/sbin/nologin")},
	{"WithLoginClass", WithLoginClass("daemon"), []string{"LocalUser"}, one("SetLoginClass", "daemon")},
	{"WithClass", WithClass("staff"), []string{"LocalUser"}, one("SetLoginClass", "staff")},
	{"WithPrimaryGroup", WithPrimaryGroup("svc"), []string{"LocalUser"}, one("SetGroup", "svc")},
	{"WithSystem", WithSystem, []string{"LocalUser"}, one("SetSystem", nil)},
	{"WithSupplementaryGroups", WithSupplementaryGroups("audio", "video"), []string{"LocalUser"}, one("AddSupplementaryGroups", []string{"audio", "video"})},
	{"WithUserGroup", WithUserGroup("wheel"), []string{"LocalUser"}, one("AddSupplementaryGroups", []string{"wheel"})},
	{"WithMode", WithMode(0o644), famFileDir, one("SetMode", os.FileMode(0o644))},
	{"Perm", Perm(0o750, "svc:staff"), famFileDir, []setterCall{
		{method: "SetMode", value: os.FileMode(0o750)},
		{method: "SetOwner", value: "svc"},
		{method: "SetGroup", value: "staff"},
	}},
	// The matrix recorder implements every setter, SetFileMode included, so
	// the Root* options see a directory; api's TestRootPermSugar pins the
	// file modes against real File and Dir resources.
	{"RootOwned", RootOwned, famFileDir, rootPermCalls(0o755)},
	{"RootExec", RootExec, famFileDir, rootPermCalls(0o755)},
	{"RootPrivate", RootPrivate, famFileDir, rootPermCalls(0o700)},
	{"WithSource", WithSource("/srv/src"), famFileDir, one("SetSource", "/srv/src")},
	{"WithSourceGlob", WithSourceGlob("*.conf"), []string{"Dir"}, one("SetSourceGlob", "*.conf")},
	{"WithParam", WithParam("stable"), []string{"File"}, one("SetParam", "stable")},
	{"WithTemplate", WithTemplate, []string{"File"}, one("SetTemplate", nil)},
	{"WithTemplateData", WithTemplateData(map[string]any{"k": 1}), []string{"File"}, one("SetTemplateData", map[string]any{"k": 1})},
	{"WithValidation", WithValidation("nginx", []string{"-t", CandidatePath}), []string{"File"}, one("SetValidation", []any{"nginx", []string{"-t", CandidatePath}})},
	{"WithSourceBase", WithSourceBase("assets"), []string{"Dir"}, one("SetSourceBase", "assets")},
	{"WithContent", WithContent("hello"), []string{"File"}, one("SetContent", "hello")},
	{"WithContentFrom", WithContentFrom("hello", nil), []string{"File"}, one("SetContent", "hello")},
	{"WithShellVar", WithShellVar("k", "v"), []string{"File"}, one("SetKeyedLine", []any{"k=", `k="v"`})},
	{"WithLines", WithLines("a", "b"), []string{"File"}, one("AddLines", []string{"a", "b"})},
	{"WithoutLines", WithoutLines("c", "d"), []string{"File"}, one("RemoveLines", []string{"c", "d"})},
	{"WithLine", WithLine("e"), []string{"File"}, one("SetAddLine", "e")},
	{"WithoutLine", WithoutLine("f"), []string{"File"}, one("SetRemoveLine", "f")},
	{"WithKeyedLine", WithKeyedLine("k=", "k=v"), []string{"File"}, one("SetKeyedLine", []any{"k=", "k=v"})},
	{"WithBlock", WithBlock("hosts", "a", "b"), []string{"File"}, one("SetBlock", []any{"hosts", []string{"a", "b"}})},
	{"WithFileMode", WithFileMode(0o600), []string{"Dir"}, one("SetFileMode", os.FileMode(0o600))},
	{"WithPrune", WithPrune, []string{"Dir"}, one("SetPrune", nil)},
	{"IsAbsent", IsAbsent, famAbsent, one("SetAbsent", nil)},
	{"IsLatest", IsLatest, []string{"Package"}, one("SetLatest", nil)},
	{"WithRestart", WithRestart, famServiceTimer, one("SetRestart", nil)},
	{"WithReload", WithReload, []string{"Service"}, one("SetReload", nil)},
	{"WithUser", WithUser, famUser, one("SetUser", nil)},
	{"WithElevate", WithElevate, []string{"Command"}, one("SetElevate", nil)},
	{"WithEnableOnly", WithEnableOnly, famEnableOnly, one("SetEnableOnly", nil)},
	{"IfChanged", IfChanged, []string{"DaemonReload"}, one("SetChangeWatch", []string(nil))},
	{"WithWatch", WithWatch("x", "y"), []string{"DaemonReload"}, one("SetWatch", []string{"x", "y"})},
	{"WatchChanges", WatchChanges("File[w]"), famChangeGate, one("SetChangeWatch", []string{"File[w]"})},
	{"WithCronUser", WithCronUser("root"), []string{"Cron"}, one("SetCronUser", "root")},
	{"WithLegacyCommand", WithLegacyCommand("/bin/old"), []string{"Cron"}, one("SetLegacyCommand", "/bin/old")},
	{"WithCommand", WithCommand("true"), famCronTimer, one("SetCommand", "true")},
	{"WithMinute", WithMinute("5"), []string{"Cron"}, one("SetMinute", "5")},
	{"WithHour", WithHour("6"), []string{"Cron"}, one("SetHour", "6")},
	{"WithMonthday", WithMonthday("7"), []string{"Cron"}, one("SetMonthday", "7")},
	{"WithMonth", WithMonth("8"), []string{"Cron"}, one("SetMonth", "8")},
	{"WithWeekday", WithWeekday("1"), []string{"Cron"}, one("SetWeekday", "1")},
	{"WithCronEnv", WithCronEnv("K=V"), []string{"Cron"}, one("AddCronEnv", "K=V")},
	{"WithSchedule", WithSchedule("10 6 * * *"), []string{"Cron"}, one("SetSchedule", "10 6 * * *")},
	{"WithFlags", WithFlags("-v"), []string{"Service"}, one("SetFlags", "-v")},
	{"WithOnCalendar", WithOnCalendar("daily"), []string{"SystemdTimer"}, one("SetOnCalendar", "daily")},
	{"WithOnBootSec", WithOnBootSec("5min"), []string{"SystemdTimer"}, one("SetOnBootSec", "5min")},
	{"WithPersistent", WithPersistent, []string{"SystemdTimer"}, one("SetPersistent", nil)},
	{"WithDescription", WithDescription("timer"), []string{"SystemdTimer"}, one("SetDescription", "timer")},
	{"WithServiceDescription", WithServiceDescription("svc"), []string{"SystemdTimer"}, one("SetServiceDescription", "svc")},
	{"WithAfter", WithAfter("network.target", "time-sync.target"), []string{"SystemdTimer"}, one("AddAfter", []string{"network.target", "time-sync.target"})},
	{"WithWants", WithWants("network-online.target"), []string{"SystemdTimer"}, one("AddWants", []string{"network-online.target"})},
	{"WithSymlink", WithSymlink("/sym"), []string{"Link"}, one("SetSymlink", "/sym")},
	{"WithHardlink", WithHardlink("/hard"), []string{"Link"}, one("SetHardlink", "/hard")},
	{"WithName", WithName("renamed"), []string{"File", "Command"}, one("SetName", "renamed")},
	{"WithDir", WithDir("/work"), []string{"Command"}, one("SetDir", "/work")},
	{"WithEnv", WithEnv(map[string]string{"A": "B"}), []string{"Package", "Command"}, one("SetEnv", map[string]string{"A": "B"})},
	{"Creates", Creates("/marker"), []string{"Command"}, one("SetCreates", "/marker")},
	{"Unless", Unless("test", []string{"-e", "/x"}), []string{"Command"}, one("SetUnless", &Guard{Name: "test", Args: []string{"-e", "/x"}})},
	{"OnlyIf", OnlyIf("probe", nil, ExpectExit(2), ExpectStdout("ok")), []string{"Command"}, one("SetOnlyIf", &Guard{Name: "probe", ExpectExit: 2, ExpectStdout: "ok"})},
	{"ConfigFile", ConfigFile("aliases", "/etc/mail/aliases", WithContent("x"), WithMode(0o644)), []string{"ConfigSet"},
		one("AddMember", []any{"aliases", "/etc/mail/aliases", 2})},
	{"WithSetValidation", WithSetValidation("smtpd", []string{"-n", "-f", MemberPath("smtpd.conf")}), []string{"ConfigSet"},
		one("AddSetValidator", []any{"smtpd", []string{"-n", "-f", MemberPath("smtpd.conf")}})},
	{"WithChroot", WithChroot("/var/nsd"), []string{"ConfigSet"}, one("SetChroot", "/var/nsd")},
	{"WithStagingDir", WithStagingDir("/etc/mail"), []string{"ConfigSet"}, one("SetStagingDir", "/etc/mail")},
	{"WithSensitive", WithSensitive, famSensitive, one("SetSensitive", nil)},
	{"OnChange", OnChange(fileA), famChangeGate, []setterCall{
		{method: "SetChangeWatch", value: []string{"File[a]"}},
		{method: "AddDependency", value: "File[a]"},
	}},
}

// TestOptionFamilyMatrix applies every option through every family interface
// it implements (the path a resource constructor takes: opt.Apply(resource))
// and asserts both halves of the contract: the option implements exactly the
// expected families, and each Apply reaches the right setter with the right
// value. The compile-time tests in options_test.go prove acceptance; this one
// also proves REJECTION, so widening an option to an extra family is caught.
func TestOptionFamilyMatrix(t *testing.T) {
	for _, tc := range optionCases {
		t.Run(tc.name, func(t *testing.T) {
			var accepted []string
			for _, fam := range allFamilies {
				apply, ok := fam.as(tc.option)
				if !ok {
					continue
				}
				accepted = append(accepted, fam.name)
				target := &recorder{}
				apply(target)
				if !reflect.DeepEqual(target.calls, tc.want) {
					t.Errorf("via %sOption: calls = %#v, want %#v", fam.name, target.calls, tc.want)
				}
			}
			if !slices.Equal(accepted, tc.families) {
				t.Errorf("families = %v, want %v", accepted, tc.families)
			}
		})
	}
}

// rootPermCalls is the setter sequence of Perm(mode, Root).
func rootPermCalls(mode os.FileMode) []setterCall {
	return []setterCall{
		{method: "SetMode", value: mode},
		{method: "SetOwner", value: "root"},
		{method: "SetGroup", value: "0"},
	}
}
