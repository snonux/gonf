package options

import "os"

// recorder is a fake resource implementing every capability interface of
// this package, recording each setter call in order. Options must stay
// resource-agnostic, so the behaviour tests apply them to this fake. The
// concrete resource types are covered separately: resource_capability_test.go
// checks each one has every setter its family's options call (with the
// recorder's signatures as the reference), and api/plan_option_fitness_test.go
// exercises them end to end through the plan wire.
type recorder struct {
	calls []setterCall
}

// setterCall is one recorded setter invocation: the method name and the value
// it received (nil for value-less setters).
type setterCall struct {
	method string
	value  any
}

func (r *recorder) record(method string, value any) {
	r.calls = append(r.calls, setterCall{method: method, value: value})
}

func (r *recorder) SetOwner(v string)          { r.record("SetOwner", v) }
func (r *recorder) SetGroup(v string)          { r.record("SetGroup", v) }
func (r *recorder) SetMode(v os.FileMode)      { r.record("SetMode", v) }
func (r *recorder) SetSource(v string)         { r.record("SetSource", v) }
func (r *recorder) SetSourceGlob(v string)     { r.record("SetSourceGlob", v) }
func (r *recorder) SetSourceBase(v string)     { r.record("SetSourceBase", v) }
func (r *recorder) SetParam(v string)          { r.record("SetParam", v) }
func (r *recorder) SetTemplate()               { r.record("SetTemplate", nil) }
func (r *recorder) SetTemplateData(v any)      { r.record("SetTemplateData", v) }
func (r *recorder) SetContent(v string)        { r.record("SetContent", v) }
func (r *recorder) SetAddLine(v string)        { r.record("SetAddLine", v) }
func (r *recorder) SetRemoveLine(v string)     { r.record("SetRemoveLine", v) }
func (r *recorder) AddLines(v ...string)       { r.record("AddLines", v) }
func (r *recorder) RemoveLines(v ...string)    { r.record("RemoveLines", v) }
func (r *recorder) SetFileMode(v os.FileMode)  { r.record("SetFileMode", v) }
func (r *recorder) SetPrune()                  { r.record("SetPrune", nil) }
func (r *recorder) SetAbsent()                 { r.record("SetAbsent", nil) }
func (r *recorder) SetLatest()                 { r.record("SetLatest", nil) }
func (r *recorder) AddDependency(v string)     { r.record("AddDependency", v) }
func (r *recorder) SetName(v string)           { r.record("SetName", v) }
func (r *recorder) SetDir(v string)            { r.record("SetDir", v) }
func (r *recorder) SetEnv(v map[string]string) { r.record("SetEnv", v) }
func (r *recorder) SetCreates(v string)        { r.record("SetCreates", v) }
func (r *recorder) SetUnless(v *Guard)         { r.record("SetUnless", v) }
func (r *recorder) SetOnlyIf(v *Guard)         { r.record("SetOnlyIf", v) }
func (r *recorder) SetSymlink(v string)        { r.record("SetSymlink", v) }
func (r *recorder) SetHardlink(v string)       { r.record("SetHardlink", v) }
func (r *recorder) SetRestart()                { r.record("SetRestart", nil) }
func (r *recorder) SetReload()                 { r.record("SetReload", nil) }
func (r *recorder) SetUser()                   { r.record("SetUser", nil) }
func (r *recorder) SetEnableOnly()             { r.record("SetEnableOnly", nil) }
func (r *recorder) SetChangeWatch(v []string)  { r.record("SetChangeWatch", v) }
func (r *recorder) SetWatch(v []string)        { r.record("SetWatch", v) }
func (r *recorder) SetElevate()                { r.record("SetElevate", nil) }
func (r *recorder) SetCronUser(v string)       { r.record("SetCronUser", v) }
func (r *recorder) SetLegacyCommand(v string)  { r.record("SetLegacyCommand", v) }
func (r *recorder) SetCommand(v string)        { r.record("SetCommand", v) }
func (r *recorder) SetMinute(v string)         { r.record("SetMinute", v) }
func (r *recorder) SetHour(v string)           { r.record("SetHour", v) }
func (r *recorder) SetMonthday(v string)       { r.record("SetMonthday", v) }
func (r *recorder) SetMonth(v string)          { r.record("SetMonth", v) }
func (r *recorder) SetWeekday(v string)        { r.record("SetWeekday", v) }
func (r *recorder) AddCronEnv(v string)        { r.record("AddCronEnv", v) }
func (r *recorder) SetHome(v string)           { r.record("SetHome", v) }
func (r *recorder) SetCreateHome()             { r.record("SetCreateHome", nil) }
func (r *recorder) SetShell(v string)          { r.record("SetShell", v) }
func (r *recorder) SetLoginClass(v string)     { r.record("SetLoginClass", v) }
func (r *recorder) SetSystem()                 { r.record("SetSystem", nil) }
func (r *recorder) SetManageHome()             { r.record("SetManageHome", nil) }
func (r *recorder) SetOnCalendar(v string)     { r.record("SetOnCalendar", v) }
func (r *recorder) SetOnBootSec(v string)      { r.record("SetOnBootSec", v) }
func (r *recorder) SetPersistent()             { r.record("SetPersistent", nil) }
func (r *recorder) SetDescription(v string)    { r.record("SetDescription", v) }
func (r *recorder) AddAfter(v ...string)       { r.record("AddAfter", v) }
func (r *recorder) AddWants(v ...string)       { r.record("AddWants", v) }
func (r *recorder) SetValidation(bin string, args []string) {
	r.record("SetValidation", []any{bin, args})
}
func (r *recorder) AddSupplementaryGroups(v ...string) {
	r.record("AddSupplementaryGroups", v)
}
func (r *recorder) SetServiceDescription(v string) {
	r.record("SetServiceDescription", v)
}

// Config-set setters. AddMember records the key, path and the number of file
// options (option funcs are not comparable, the member options themselves are
// covered by the File rows).
func (r *recorder) AddMember(key, path string, opts []FileOption) {
	r.record("AddMember", []any{key, path, len(opts)})
}
func (r *recorder) AddSetValidator(bin string, args []string) {
	r.record("AddSetValidator", []any{bin, args})
}
func (r *recorder) SetChroot(v string)     { r.record("SetChroot", v) }
func (r *recorder) SetStagingDir(v string) { r.record("SetStagingDir", v) }

// SetSensitive is the WithSensitive setter (sensitive.go).
func (r *recorder) SetSensitive() { r.record("SetSensitive", nil) }

// Compile-time proof that recorder implements each capability listed here: a
// listed capability whose setter changes signature fails to build. A NEW
// capability missing from both this list and the recorder is caught instead
// by TestRecorderImplementsEveryCapability (capability_test.go).
var (
	_ Owner                  = (*recorder)(nil)
	_ MemberAddable          = (*recorder)(nil)
	_ SetValidatable         = (*recorder)(nil)
	_ Chrootable             = (*recorder)(nil)
	_ StagingDirable         = (*recorder)(nil)
	_ Grouped                = (*recorder)(nil)
	_ Moded                  = (*recorder)(nil)
	_ Sourced                = (*recorder)(nil)
	_ SourceGlobable         = (*recorder)(nil)
	_ SourceBaseable         = (*recorder)(nil)
	_ Paramable              = (*recorder)(nil)
	_ Templateable           = (*recorder)(nil)
	_ TemplateDataable       = (*recorder)(nil)
	_ Validatable            = (*recorder)(nil)
	_ Contented              = (*recorder)(nil)
	_ LineAddable            = (*recorder)(nil)
	_ LineRemovable          = (*recorder)(nil)
	_ LinesAddable           = (*recorder)(nil)
	_ LinesRemovable         = (*recorder)(nil)
	_ FileModed              = (*recorder)(nil)
	_ Prunable               = (*recorder)(nil)
	_ Absentable             = (*recorder)(nil)
	_ Latestable             = (*recorder)(nil)
	_ Dependable             = (*recorder)(nil)
	_ Named                  = (*recorder)(nil)
	_ Dirable                = (*recorder)(nil)
	_ Envable                = (*recorder)(nil)
	_ Creatable              = (*recorder)(nil)
	_ Guardable              = (*recorder)(nil)
	_ Linkable               = (*recorder)(nil)
	_ Restartable            = (*recorder)(nil)
	_ Reloadable             = (*recorder)(nil)
	_ UserService            = (*recorder)(nil)
	_ EnableOnlyable         = (*recorder)(nil)
	_ ChangeGated            = (*recorder)(nil)
	_ Watchable              = (*recorder)(nil)
	_ ChangeWatchable        = (*recorder)(nil)
	_ Elevatable             = (*recorder)(nil)
	_ CronUserable           = (*recorder)(nil)
	_ LegacyCronCommandable  = (*recorder)(nil)
	_ Commandable            = (*recorder)(nil)
	_ Minuteable             = (*recorder)(nil)
	_ Hourable               = (*recorder)(nil)
	_ Monthdayable           = (*recorder)(nil)
	_ Monthable              = (*recorder)(nil)
	_ Weekdayable            = (*recorder)(nil)
	_ CronEnvable            = (*recorder)(nil)
	_ Homeable               = (*recorder)(nil)
	_ CreateHomeable         = (*recorder)(nil)
	_ Shellable              = (*recorder)(nil)
	_ Classable              = (*recorder)(nil)
	_ Systemable             = (*recorder)(nil)
	_ SupplementaryGroupable = (*recorder)(nil)
	_ HomeManageable         = (*recorder)(nil)
	_ OnCalendarable         = (*recorder)(nil)
	_ OnBootSecable          = (*recorder)(nil)
	_ Persistentable         = (*recorder)(nil)
	_ Descriptionable        = (*recorder)(nil)
	_ ServiceDescriptionable = (*recorder)(nil)
	_ Afterable              = (*recorder)(nil)
	_ Wantsable              = (*recorder)(nil)
	_ Sensitivable           = (*recorder)(nil)
)
