//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var binName = "gonf"

func run(cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// Default runs the program.
func Default() error {
	return Run()
}

// Build compiles the binary.
func Build() error {
	fmt.Println("building...")
	return run("go", "build", "-o", binName, "./cmd/gonf")
}

// Run builds and runs the program.
func Run() error {
	fmt.Println("running...")
	if err := Build(); err != nil {
		return err
	}
	return run("./"+binName, "-version")
}

// Test runs all unit tests with the race detector enabled: the codebase has
// real concurrency (fleet errgroup fan-out, mutex-guarded global registries,
// swapped test seams) that only -race exercises. -shuffle=on randomizes test
// (and top-level subtest) execution order within a package, catching
// test-isolation bugs like leaked global state between tests.
func Test() error {
	fmt.Println("testing...")
	return run("go", "test", "-race", "-shuffle=on", "-v", "-count=1", "./...")
}

// planFuzzTime is the CI budget per fuzz target (overridable via GONF_FUZZTIME).
const defaultPlanFuzzTime = "30s"

// planCodecCoverMin is the minimum statement coverage required for the plan
// wire codec (codec.go + types.go), matching the remote-plan testing bar.
const planCodecCoverMin = 90.0

// TestPlanFuzz runs plan package fuzz targets for a fixed time budget.
func TestPlanFuzz() error {
	fuzzTime := os.Getenv("GONF_FUZZTIME")
	if fuzzTime == "" {
		fuzzTime = defaultPlanFuzzTime
	}
	fmt.Printf("fuzzing plan codec (%s each)...\n", fuzzTime)
	targets := []string{"FuzzDecodeOp", "FuzzDecodePlan", "FuzzRoundTripOpJSON"}
	for _, name := range targets {
		fmt.Println(" ", name)
		if err := run("go", "test", "./plan/", "-fuzz="+name, "-fuzztime="+fuzzTime); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// CoverPlan runs ./plan coverage and fails if codec.go+types.go are below
// planCodecCoverMin.
func CoverPlan() error {
	fmt.Printf("plan codec coverage (min %.1f%% on codec.go+types.go)...\n", planCodecCoverMin)
	coverOut := filepath.Join(os.TempDir(), "gonf-plan.cov")
	if err := run("go", "test", "./plan/", "-coverprofile="+coverOut, "-count=1"); err != nil {
		return err
	}
	out, err := exec.Command("go", "tool", "cover", "-func="+coverOut).Output()
	if err != nil {
		return fmt.Errorf("cover -func: %w", err)
	}
	pct, err := parseCodecCoverage(string(out))
	if err != nil {
		return err
	}
	fmt.Printf("plan codec coverage: %.1f%%\n", pct)
	if pct < planCodecCoverMin {
		return fmt.Errorf("plan codec coverage %.1f%% is below required %.1f%%", pct, planCodecCoverMin)
	}
	return nil
}

// CheckPlan runs unit tests for ./plan, coverage gate, then fuzz budget.
func CheckPlan() error {
	if err := run("go", "test", "-count=1", "./plan/"); err != nil {
		return err
	}
	if err := CoverPlan(); err != nil {
		return err
	}
	return TestPlanFuzz()
}

func parseCodecCoverage(coverFunc string) (float64, error) {
	var sum float64
	var n int
	for _, line := range strings.Split(coverFunc, "\n") {
		if !strings.Contains(line, "/plan/codec.go:") && !strings.Contains(line, "/plan/types.go:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pctStr := strings.TrimSuffix(fields[len(fields)-1], "%")
		var pct float64
		if _, err := fmt.Sscanf(pctStr, "%f", &pct); err != nil {
			return 0, fmt.Errorf("parse %q: %w", pctStr, err)
		}
		sum += pct
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no codec.go/types.go entries in cover output")
	}
	return sum / float64(n), nil
}

// TestDNF runs DNF-specific integration tests. This requires root privileges.
func TestDNF() error {
	fmt.Println("testing DNF integration...")
	// Use 'env' to set the variable for the go test command
	return run("env", "GONF_RUN_DNF_TESTS=1", "go", "test", "-v", "-count=1", "./resource/pkg/...")
}

// Lint runs gofmt's formatting check, go vet, and staticcheck.
func Lint() error {
	fmt.Println("linting...")
	if err := lintGofmt(); err != nil {
		return err
	}
	if err := run("go", "vet", "./..."); err != nil {
		return err
	}
	// staticcheck is pinned as a Go tool dependency (see the "tool" line in
	// go.mod), so "go tool staticcheck" always resolves the same version
	// without requiring a separate global install, locally or in CI.
	return run("go", "tool", "staticcheck", "./...")
}

// lintGofmt fails with the list of offending files if any .go file (outside
// vendored/generated paths, of which this repo currently has none) is not
// gofmt-formatted. "gofmt -l" only lists file names and exits 0 even when it
// finds unformatted files, so unlike go vet/staticcheck its failure has to be
// derived from its output rather than its exit code.
func lintGofmt() error {
	out, err := exec.Command("gofmt", "-l", ".").Output()
	if err != nil {
		return fmt.Errorf("gofmt -l: %w", err)
	}
	files := strings.TrimSpace(string(out))
	if files == "" {
		return nil
	}
	return fmt.Errorf("gofmt -l found unformatted files (run 'gofmt -w'):\n%s", files)
}

// Install builds and installs the binary to $GOPATH/bin.
func Install() error {
	fmt.Println("installing...")
	return run("go", "install", "./cmd/gonf")
}

// Uninstall removes the binary from $GOPATH/bin.
func Uninstall() error {
	fmt.Println("uninstalling...")
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(os.Getenv("HOME"), "go")
	}
	binPath := filepath.Join(gopath, "bin", binName)
	if err := os.Remove(binPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", binPath, err)
		}
		fmt.Println("binary not found, nothing to remove")
	} else {
		fmt.Println("removed " + binPath)
	}
	return nil
}
