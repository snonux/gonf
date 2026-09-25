package pathtoken

import (
	"errors"
	"strings"
	"testing"
)

// stubUserHome replaces the user-database lookup for one test.
func stubUserHome(t *testing.T, home string, err error) {
	t.Helper()
	prev := currentUserHome
	currentUserHome = func() (string, error) { return home, err }
	t.Cleanup(func() { currentUserHome = prev })
}

func TestExpandHome(t *testing.T) {
	t.Setenv("HOME", "/dest/home")
	for in, want := range map[string]string{
		"/etc/x":           "/etc/x",
		Home:               "/dest/home",
		Home + "/.bashrc":  "/dest/home/.bashrc",
		"literal$dollar":   "literal$dollar",
		"a/" + Home + "/b": "a//dest/home/b",
	} {
		got, err := Expand(in)
		if err != nil || got != want {
			t.Errorf("Expand(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

// TestExpandHomeFallsBackToUserDatabase: with $HOME unset the applying
// user's passwd entry supplies the home.
func TestExpandHomeFallsBackToUserDatabase(t *testing.T) {
	t.Setenv("HOME", "")
	stubUserHome(t, "/from/passwd", nil)
	got, err := Expand(Home + "/x")
	if err != nil || got != "/from/passwd/x" {
		t.Fatalf("Expand = %q, %v; want /from/passwd/x", got, err)
	}
}

// TestExpandHomeRefusesUnusableHome: an empty or relative home, or a failed
// lookup, never silently retargets the path.
func TestExpandHomeRefusesUnusableHome(t *testing.T) {
	cases := []struct {
		name, env, db string
		dbErr         error
		want          string
	}{
		{"empty everywhere", "", "", nil, "home directory not set"},
		{"lookup fails", "", "", errors.New("no passwd"), "user lookup failed"},
		{"relative env", "rel/home", "", nil, "not absolute"},
		{"relative db", "", "rel", nil, "not absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", tc.env)
			stubUserHome(t, tc.db, tc.dbErr)
			got, err := Expand(Home + "/x")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Expand = %q, %v; want error containing %q", got, err, tc.want)
			}
		})
	}
}

func TestHasToken(t *testing.T) {
	t.Parallel()
	if !HasToken(Home+"/x") || !HasToken("${FOO}") || HasToken("/etc/$x") {
		t.Fatal("HasToken mismatch")
	}
}
