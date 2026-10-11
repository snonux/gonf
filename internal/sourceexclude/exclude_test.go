package sourceexclude

import "testing"

func TestPatterns(t *testing.T) {
	for _, tc := range []struct {
		pattern, rel string
		want         bool
	}{
		{"cache", "sub/cache/a", true}, {"sub/cache", "sub/cache/a", true},
		{"sub/*.tmp", "sub/a.tmp", true}, {"sub/*.tmp", "sub/nested/a.tmp", false},
		{"*.tmp", "sub/nested/a.tmp", true}, {"a?", "ab", true},
		{"[ab].conf", "b.conf", true}, {"private", "private.tmpl", true},
		{"private.tmpl", "private", false}, {"cache", ".", false}, {"cache", "", false},
		{"cache", "other/a", false},
	} {
		t.Run(tc.pattern+":"+tc.rel, func(t *testing.T) {
			if err := Validate([]string{tc.pattern}); err != nil {
				t.Fatal(err)
			}
			if got := Matches([]string{tc.pattern}, tc.rel); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	for _, p := range []string{"", "/tmp", "../x", "a/../x", "a/./x", "a//x", "a/", "a\\b", "**/x", "["} {
		if err := Validate([]string{p}); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
	if Matches([]string{"["}, "x") {
		t.Fatal("invalid pattern matched")
	}
}
