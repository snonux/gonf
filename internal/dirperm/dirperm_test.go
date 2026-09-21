package dirperm

import (
	"os"
	"testing"
)

func TestIsPrivateGroup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ids  IDs
		gid  uint32
		want bool
	}{
		{"own private group", IDs{EUID: 1000, EGID: 1000}, 1000, true},
		{"other group", IDs{EUID: 1000, EGID: 1000}, 100, false},
		{"shared primary group", IDs{EUID: 1000, EGID: 100}, 100, false},
		{"root never", IDs{EUID: 0, EGID: 0}, 0, false},
		{"root, another gid", IDs{EUID: 0, EGID: 0}, 1, false},
		{"egid == euid, gid differs", IDs{EUID: 1000, EGID: 1000}, 1001, false},
		// egid differs from euid (after newgrp/sg): a group whose number
		// happens to equal the uid is not the caller's private group.
		{"uid-numbered group with foreign egid", IDs{EUID: 1000, EGID: 100}, 1000, false},
		{"egid 0 for a non-root user", IDs{EUID: 1000, EGID: 0}, 0, false},
	}
	for _, tc := range cases {
		if got := tc.ids.IsPrivateGroup(tc.gid); got != tc.want {
			t.Errorf("%s: IsPrivateGroup(%d) = %v, want %v", tc.name, tc.gid, got, tc.want)
		}
	}
}

func TestCurrent(t *testing.T) {
	t.Parallel()
	ids := Current()
	if int(ids.EUID) != os.Geteuid() || int(ids.EGID) != os.Getegid() {
		t.Fatalf("Current() = %+v, want euid %d egid %d", ids, os.Geteuid(), os.Getegid())
	}
}
