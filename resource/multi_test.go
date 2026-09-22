package resource

import (
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
