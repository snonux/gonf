package platform

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestSupportedAndBSD(t *testing.T) {
	t.Parallel()
	want := []string{"linux", "darwin", "freebsd", "openbsd", "netbsd"}
	if got := Supported(); !slices.Equal(got, want) {
		t.Fatalf("Supported() = %v, want %v", got, want)
	}
	for _, b := range BSD() {
		if !IsSupported(b) {
			t.Errorf("BSD %q is not in Supported()", b)
		}
	}
	if got := List(); got != strings.Join(want, ", ") {
		t.Errorf("List() = %q", got)
	}
}

// TestSupportedReturnsCopies: callers cannot corrupt the shared list.
func TestSupportedReturnsCopies(t *testing.T) {
	t.Parallel()
	s, b := Supported(), BSD()
	s[0], b[0] = "plan9", "plan9"
	if IsSupported("plan9") || slices.Contains(BSD(), "plan9") {
		t.Fatal("mutating a returned slice changed the shared list")
	}
}

func TestCheckAcceptsEverySupported(t *testing.T) {
	t.Parallel()
	for _, g := range Supported() {
		if err := Check(g); err != nil {
			t.Errorf("Check(%q) = %v, want nil", g, err)
		}
	}
}

func TestCheckRefuses(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":        "empty GOOS (want one of linux, darwin, freebsd, openbsd, netbsd)",
		"plan9":   `unsupported GOOS "plan9" (want one of linux, darwin, freebsd, openbsd, netbsd)`,
		"windows": `unsupported GOOS "windows" (want one of`,
		"Linux":   `unsupported GOOS "Linux" (GOOS names are lower case: did you mean "linux"?)`,
		"NETBSD":  `did you mean "netbsd"?`,
		" linux":  `unsupported GOOS " linux" (want one of`,
	}
	for in, want := range cases {
		err := Check(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Check(%q) = %v, want it to contain %q", in, err, want)
		}
		if IsSupported(in) {
			t.Errorf("IsSupported(%q) = true", in)
		}
	}
	if err := Check(""); !errors.Is(err, ErrEmpty) {
		t.Errorf("Check(\"\") = %v, want ErrEmpty", err)
	}
}

// consumers are the files that accept or derive a GOOS. They must take the
// names from this package: none may spell a managed GOOS as a string
// literal of its own (task cb), so a new OS cannot be added in one place
// and forgotten in another.
var consumers = []string{
	"api/task_goos.go",
	"inventory/inventory.go",
	"internal/remote/sync_probe.go",
}

// TestConsumersUseSharedList pins that no consumer hard-codes a managed
// GOOS name; each must import this package instead.
func TestConsumersUseSharedList(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	for _, rel := range consumers {
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !importsPlatform(f) {
			t.Errorf("%s does not import internal/platform", rel)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if v, err := strconv.Unquote(lit.Value); err == nil && IsSupported(strings.ToLower(v)) {
				t.Errorf("%s: hard-coded GOOS literal %s; use internal/platform", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}

func importsPlatform(f *ast.File) bool {
	for _, imp := range f.Imports {
		if imp.Path.Value == `"github.com/snonux/gonf/internal/platform"` {
			return true
		}
	}
	return false
}
