package resource

import (
	"errors"
	"testing"
)

func TestMultiString(t *testing.T) {
	m := Multi{
		Resource{Type: "File", Name: "/tmp/a"},
		Resource{Type: "File", Name: "/tmp/b"},
	}

	want := "File[/tmp/a], File[/tmp/b]"
	if got := m.String(); got != want {
		t.Errorf("Multi.String() = %q, want %q", got, want)
	}
}

func TestMultiID(t *testing.T) {
	m := Multi{
		Resource{Type: "File", Name: "/tmp/a"},
		Resource{Type: "File", Name: "/tmp/b"},
	}

	want := "File[/tmp/a]+File[/tmp/b]"
	if got := m.ID(); got != want {
		t.Errorf("Multi.ID() = %q, want %q", got, want)
	}
}

func TestMultiApply(t *testing.T) {
	tests := []struct {
		name     string
		appliers []Applier
		wantErr  bool
	}{
		{
			name: "all success",
			appliers: []Applier{
				ApplierFunc(func() error { return nil }),
				ApplierFunc(func() error { return nil }),
			},
			wantErr: false,
		},
		{
			name: "one failure",
			appliers: []Applier{
				ApplierFunc(func() error { return nil }),
				ApplierFunc(func() error { return errors.New("fail 1") }),
			},
			wantErr: true,
		},
		{
			name: "multiple failures",
			appliers: []Applier{
				ApplierFunc(func() error { return errors.New("fail 1") }),
				ApplierFunc(func() error { return errors.New("fail 2") }),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resources []Resource
			for _, app := range tt.appliers {
				resources = append(resources, Resource{
					applier: app,
				})
			}

			m := Multi(resources)
			err := m.Apply()

			if (err != nil) != tt.wantErr {
				t.Errorf("Multi.Apply() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
