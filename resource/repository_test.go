package resource

import (
	"errors"
	"strings"
	"testing"
)

// mockApplier records the order in which Apply was called.
type mockApplier struct {
	name string
	err  error
	logs *[]string
}

func (m *mockApplier) Apply() error {
	*m.logs = append(*m.logs, m.name)
	return m.err
}

func TestApply(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(r *repository, logs *[]string)
		wantOrder   []string
		wantError   bool
		errorString string
	}{
		{
			name: "dependency chain",
			setup: func(r *repository, logs *[]string) {
				// A depends on B, B depends on C
				r.registered["A"] = Resource{
					Type: "T", Name: "A",
					applier: &mockApplier{name: "A", logs: logs},
					dependsOn: map[string]struct{}{"B": {}},
				}
				r.registered["B"] = Resource{
					Type: "T", Name: "B",
					applier: &mockApplier{name: "B", logs: logs},
					dependsOn: map[string]struct{}{"C": {}},
				}
				r.registered["C"] = Resource{
					Type: "T", Name: "C",
					applier: &mockApplier{name: "C", logs: logs},
				}
			},
			wantOrder: []string{"C", "B", "A"},
		},
		{
			name: "independent resources apply in sorted order",
			setup: func(r *repository, logs *[]string) {
				// Registered/visited in a deterministic, sorted order
				// regardless of map iteration.
				r.registered["C"] = Resource{
					Type: "T", Name: "C",
					applier: &mockApplier{name: "C", logs: logs},
				}
				r.registered["A"] = Resource{
					Type: "T", Name: "A",
					applier: &mockApplier{name: "A", logs: logs},
				}
				r.registered["B"] = Resource{
					Type: "T", Name: "B",
					applier: &mockApplier{name: "B", logs: logs},
				}
			},
			wantOrder: []string{"A", "B", "C"},
		},
		{
			name: "diamond dependency",
			setup: func(r *repository, logs *[]string) {
				// D depends on B and C; both B and C depend on A. A must run
				// once, before B and C, which run before D.
				r.registered["A"] = Resource{
					Type: "T", Name: "A",
					applier: &mockApplier{name: "A", logs: logs},
				}
				r.registered["B"] = Resource{
					Type: "T", Name: "B",
					applier:   &mockApplier{name: "B", logs: logs},
					dependsOn: map[string]struct{}{"A": {}},
				}
				r.registered["C"] = Resource{
					Type: "T", Name: "C",
					applier:   &mockApplier{name: "C", logs: logs},
					dependsOn: map[string]struct{}{"A": {}},
				}
				r.registered["D"] = Resource{
					Type: "T", Name: "D",
					applier:   &mockApplier{name: "D", logs: logs},
					dependsOn: map[string]struct{}{"B": {}, "C": {}},
				}
			},
			wantOrder: []string{"A", "B", "C", "D"},
		},
		{
			name: "circular dependency",
			setup: func(r *repository, logs *[]string) {
				r.registered["A"] = Resource{
					Type: "T", Name: "A",
					applier: &mockApplier{name: "A", logs: logs},
					dependsOn: map[string]struct{}{"B": {}},
				}
				r.registered["B"] = Resource{
					Type: "T", Name: "B",
					applier: &mockApplier{name: "B", logs: logs},
					dependsOn: map[string]struct{}{"A": {}},
				}
			},
			wantError:   true,
			errorString: "circular dependency",
		},
		{
			name: "missing dependency",
			setup: func(r *repository, logs *[]string) {
				r.registered["A"] = Resource{
					Type: "T", Name: "A",
					applier: &mockApplier{name: "A", logs: logs},
					dependsOn: map[string]struct{}{"Missing": {}},
				}
			},
			wantError:   true,
			errorString: "not registered",
		},
		{
			name: "execution failure",
			setup: func(r *repository, logs *[]string) {
				r.registered["A"] = Resource{
					Type: "T", Name: "A",
					applier: &mockApplier{name: "A", err: errors.New("fail A"), logs: logs},
				}
			},
			wantError:   true,
			errorString: "failed to apply",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ResetRepository()
			logs := make([]string, 0)
			r := getRepository()
			tt.setup(r, &logs)

			err := Apply()

			if (err != nil) != tt.wantError {
				t.Errorf("Apply() error = %v, wantError %v", err, tt.wantError)
				return
			}

			if tt.wantError && tt.errorString != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errorString) {
					t.Errorf("Apply() error = %v, want error containing %q", err, tt.errorString)
				}
			}

			if !tt.wantError && tt.wantOrder != nil {
				if len(logs) != len(tt.wantOrder) {
					t.Errorf("Apply() log length = %d, want %d", len(logs), len(tt.wantOrder))
					return
				}
				for i := range logs {
					if logs[i] != tt.wantOrder[i] {
						t.Errorf("Apply() order = %v, want %v", logs, tt.wantOrder)
						break
					}
				}
			}
		})
	}
}
