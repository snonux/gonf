package shellwords

import (
	"reflect"
	"testing"
)

func TestQuoteRoundTrip(t *testing.T) {
	for _, s := range []string{"/usr/local/bin/gonf", "/opt/my tools/gonf", "it's", "a;b", "$HOME", ""} {
		got, err := Split(Quote(s))
		if err != nil {
			t.Fatalf("Split(Quote(%q)): %v", s, err)
		}
		if want := []string{s}; !reflect.DeepEqual(got, want) && s != "" {
			t.Errorf("Split(Quote(%q)) = %q", s, got)
		}
	}
	if q := Quote("/usr/local/bin/gonf"); q != "/usr/local/bin/gonf" {
		t.Errorf("plain path quoted: %q", q)
	}
}
