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

// TestExpandHomeIsCleaned: a valid but unclean home (HOME=/ for service
// accounts, trailing or duplicate separators, "." or ".." elements) expands
// a clean token path to a clean path (task fb). Text around the token is
// copied verbatim, so an already unclean input stays unclean.
func TestExpandHomeIsCleaned(t *testing.T) {
	cases := []struct{ home, in, want string }{
		{"/", Home, "/"},
		{"/", Home + "/x", "/x"},
		{"/", Home + "/.config/app", "/.config/app"},
		{"/", Home + "/", "/"},
		{"/", Home + "x", "/x"},
		{"//", Home + "/x", "/x"},
		{"/home/paul/", Home, "/home/paul"},
		{"/home/paul/", Home + "/x", "/home/paul/x"},
		{"/home/paul//", Home + "/x", "/home/paul/x"},
		{"/home//paul/./", Home + "/x", "/home/paul/x"},
		{"/home/other/../paul", Home + "/x", "/home/paul/x"},
		{"/home/paul/", "a/" + Home + "/b", "a//home/paul/b"},
		// Verbatim surroundings: only the token's value is normalised,
		// and the root absorbs exactly one following separator.
		{"/home/paul/", Home + "//x", "/home/paul//x"},
		{"/", Home + "//x", "//x"},
	}
	for _, tc := range cases {
		t.Setenv("HOME", tc.home)
		got, err := Expand(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("HOME=%q: Expand(%q) = %q, %v; want %q", tc.home, tc.in, got, err, tc.want)
		}
	}
}

// TestExpandCleansUserDatabaseHome: the passwd fallback is cleaned too.
func TestExpandCleansUserDatabaseHome(t *testing.T) {
	t.Setenv("HOME", "")
	stubUserHome(t, "/", nil)
	if got, err := Expand(Home + "/x"); err != nil || got != "/x" {
		t.Fatalf("Expand = %q, %v; want /x", got, err)
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
