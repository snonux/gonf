package platform

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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

// TestFromUname maps the kernel names uname -s really prints, in any case.
func TestFromUname(t *testing.T) {
	t.Parallel()
	for sys, want := range map[string]string{
		"Linux": "linux", "Darwin": "darwin", "FreeBSD": "freebsd",
		"OpenBSD": "openbsd", "NetBSD": "netbsd",
		"LINUX": "linux", "freebsd": "freebsd", " NetBSD\n": "netbsd",
	} {
		if got, ok := FromUname(sys); !ok || got != want {
			t.Errorf("FromUname(%q) = %q, %v; want %q", sys, got, ok, want)
		}
	}
	for _, sys := range []string{"", "SunOS", "Windows_NT", "DragonFly", "GNU/Linux"} {
		if got, ok := FromUname(sys); ok {
			t.Errorf("FromUname(%q) = %q, want unsupported", sys, got)
		}
	}
}

// TestUnameTableCoversSupported: the kernel-name table maps onto supported
// exactly, so a new OS cannot be added to one and forgotten in the other.
func TestUnameTableCoversSupported(t *testing.T) {
	t.Parallel()
	var goos []string
	for _, n := range unameNames {
		goos = append(goos, n.goos)
	}
	if !slices.Equal(goos, supported) {
		t.Fatalf("unameNames GOOS = %v, want %v", goos, supported)
	}
}

func TestCheckGOARCHCase(t *testing.T) {
	t.Parallel()
	for _, a := range []string{"", "amd64", "arm64", "386", "riscv64"} {
		if err := CheckGOARCHCase(a); err != nil {
			t.Errorf("CheckGOARCHCase(%q) = %v", a, err)
		}
	}
	for in, want := range map[string]string{
		"AMD64": `GOARCH "AMD64" is not lower case: did you mean "amd64"?`,
		"Arm64": `did you mean "arm64"?`,
	} {
		if err := CheckGOARCHCase(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckGOARCHCase(%q) = %v, want %q", in, err, want)
		}
	}
}

// scanRoots are the trees whose code accepts, derives or requires a GOOS.
// Their production code must take GOOS names from this package: none may
// spell a managed GOOS (or its uname -s kernel name) as a string literal of
// its own (task cb), so a new OS cannot be added in one place and
// forgotten in another. Comments are not scanned.
var scanRoots = []string{"api", "inventory", filepath.Join("internal", "remote")}

// consumers must import this package (the shared list's direct users).
var consumers = []string{
	"api/task_goos.go",
	"api/login_class.go",
	"inventory/inventory.go",
	"internal/remote/sync_probe.go",
}

// TestConsumersUseSharedList pins that no production file under scanRoots
// hard-codes a managed GOOS name and that each consumer imports this
// package. filepath.WalkDir visits in lexical order, so the report is
// deterministic.
func TestConsumersUseSharedList(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	imports := map[string]bool{}
	scanned := 0
	for _, dir := range scanRoots {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			scanned++
			rel, _ := filepath.Rel(root, path)
			imports[filepath.ToSlash(rel)] = importsPlatform(f)
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
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no files")
	}
	for _, c := range consumers {
		if !imports[c] {
			t.Errorf("%s does not import internal/platform", c)
		}
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
