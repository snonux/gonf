//go:build mage

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var (
	binName = "gonf"
	mod     = "codeberg.org/snonux/gonf"
)

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
	return run("./"+binName, "version")
}

// Test runs all unit tests.
func Test() error {
	fmt.Println("testing...")
	return run("go", "test", "./...")
}

// Lint runs go vet.
func Lint() error {
	fmt.Println("linting...")
	return run("go", "vet", "./...")
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