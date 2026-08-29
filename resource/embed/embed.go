// Package embed holds state common to all concrete resource types (dependency
// tracking and absence marking), embedded rather than redeclared.
package embed

// DependsOn is embedded into concrete resource types to give them the ability
// to accumulate dependency IDs supplied via the DependsOn option.
type DependsOn struct {
	IDs []string
}

// AddDependency records a single resource ID this resource depends on. It uses
// a pointer receiver so the mutation is visible to the embedding value.
func (d *DependsOn) AddDependency(id string) {
	d.IDs = append(d.IDs, id)
}

// Absence is embedded into concrete resource types that can be marked for
// removal via the IsAbsent option. It promotes an Absent field and a
// SetAbsent method to the embedding type.
type Absence struct {
	Absent bool
}

// SetAbsent implements opt.Absentable, marking the resource for removal. It
// uses a pointer receiver so the mutation is visible to the embedding value.
func (a *Absence) SetAbsent() { a.Absent = true }
