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
