package embed

// Misuse is embedded into concrete resource types to collect option misuse
// (an option applied to a resource that lacks its capability, an invalid
// option value) while their options are applied. It promotes ReportMisuse
// (implements opt.MisuseReporter, which the options package calls instead of
// ending the process) and MisuseErr, which the resource's build step checks
// after applying its options, so the misuse becomes that build's error:
//
//   - a registering constructor (Present, Absent) reports it as a declaration
//     error (internal/declerr, via resource.Refuse) and registers nothing;
//   - an Ensure helper, such as a plan handler rebuilding a resource on the
//     destination, returns it, so an apply fails with an error instead of
//     silently dropping the option.
//
// The first misuse wins: a later one is usually a consequence of it.
type Misuse struct {
	err error
}

// ReportMisuse records err unless an earlier misuse is already recorded. It
// uses a pointer receiver so the mutation is visible to the embedding value.
func (m *Misuse) ReportMisuse(err error) {
	if m.err == nil {
		m.err = err
	}
}

// MisuseErr returns the first recorded misuse, or nil.
func (m *Misuse) MisuseErr() error { return m.err }
