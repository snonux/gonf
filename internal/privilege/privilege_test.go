package privilege

import "testing"

func TestWrapApplyCmd(t *testing.T) {
	got, err := WrapApplyCmd(Doas, false, "apply -")
	if err != nil || got != "gonf apply -" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = WrapApplyCmd(Doas, true, "apply -n -")
	if err != nil || got != "doas gonf apply -n -" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = WrapApplyCmd(Sudo, true, "apply -")
	if err != nil || got != "sudo -n gonf apply -" {
		t.Fatalf("%q %v", got, err)
	}
	_, err = WrapApplyCmd(None, true, "apply -")
	if err == nil {
		// may succeed if running as root in CI
		if got, _ := WrapApplyCmd(None, true, "apply -"); got != "gonf apply -" && err == nil {
			t.Fatal("expected error or root passthrough")
		}
	}
}

func TestParseMode(t *testing.T) {
	m, err := ParseMode("Doas")
	if err != nil || m != Doas {
		t.Fatal(m, err)
	}
}
