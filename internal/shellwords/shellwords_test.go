package shellwords

import (
	"slices"
	"strings"
	"testing"
)

func TestSplitWords(t *testing.T) {
	t.Parallel()
	for line, want := range map[string][]string{
		"systemctl restart foo":          {"systemctl", "restart", "foo"},
		"  a \t b  ":                     {"a", "b"},
		`echo 'a b' "c d" e\ f`:          {"echo", "a b", "c d", "e f"},
		`printf '%s\n' "x\"y\\z\$w"`:     {"printf", `%s\n`, `x"y\z$w`},
		`tr "a\b" ''`:                    {"tr", `a\b`, ""},
		`echo 'it'\''s' "#x" '*' a#b a~`: {"echo", "it's", "#x", "*", "a#b", "a~"},
		`ab"cd"'ef'`:                     {"abcdef"},
		`rsync -e "ssh -p 22" src dst:`:  {"rsync", "-e", "ssh -p 22", "src", "dst:"},
		`env A=1 B= cmd --opt=x`:         {"env", "A=1", "B=", "cmd", "--opt=x"},
	} {
		got, err := Split(line)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("Split(%q) = %q, %v; want %q", line, got, err, want)
		}
	}
}

func TestSplitRefuses(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]string{
		"":              "must not be empty",
		"   ":           "must not be empty",
		"a\nb":          "single line",
		"echo 'x":       "unterminated single quote",
		`echo "x`:       "unterminated double quote",
		`echo x\`:       "ends with a backslash",
		"a | b":         `unquoted '|'`,
		"a && b":        `unquoted '&'`,
		"a; b":          `unquoted ';'`,
		"a > f":         `unquoted '>'`,
		"a < f":         `unquoted '<'`,
		"(a)":           `unquoted '('`,
		"echo $HOME":    `unquoted '$'`,
		"echo `id`":     "unquoted '`'",
		"ls *.go":       `unquoted '*'`,
		"ls ?.go":       `unquoted '?'`,
		"ls [ab]":       `unquoted '['`,
		"echo # note":   `unquoted '#'`,
		"ls ~/x":        `unquoted '~'`,
		`echo "$HOME"`:  `'$' in double quotes`,
		"echo \"`id`\"": "'`' in double quotes",
		`echo "\$HOME"`: "",
	} {
		_, err := Split(line)
		if want == "" {
			if err != nil {
				t.Errorf("Split(%q): quoted text must stay literal, got %v", line, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Split(%q) err = %v, want %q", line, err, want)
		}
	}
}
