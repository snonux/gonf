// Command gonf-desc writes a recipe package's task descriptions from the
// task methods' doc comments, so a description is written once, next to the
// method an editor jumps to, instead of again in a DescX companion:
//
//	//go:generate go run github.com/snonux/gonf/cmd/gonf-desc
//
//	// StampDir ensures the /var/lib/unattended-upgrade stamp directory.
//	func (Unattended) StampDir() { ... }
//
// go generate then writes desc_gen.go holding
//
//	func (Unattended) DescStampDir() string { return "Ensures the /var/lib/unattended-upgrade stamp directory" }
//
// which RegisterMethods reads like a hand-written companion. The
// description is the doc comment's first sentence without the leading
// method name, starting upper case and without its final period. A task
// method is an exported method without parameters or results whose name is
// not a companion (Desc*, Opts*, When*, Opts). One without a doc comment,
// and one with a hand-written DescX companion in the package, gets no
// generated description; the hand-written one wins.
//
// Flags: -o names the output file (default desc_gen.go); -check writes
// nothing and exits 1 when the file is not up to date, for CI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/snonux/gonf/internal/gendesc"
)

func main() {
	out := flag.String("o", gendesc.DefaultFile, "output file")
	check := flag.Bool("check", false, "exit 1 when the output file is not up to date instead of writing it")
	flag.Parse()
	dir := "."
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	if err := run(dir, *out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "gonf-desc:", err)
		os.Exit(1)
	}
}

func run(dir, out string, check bool) error {
	src, err := gendesc.Generate(dir, out)
	if err != nil {
		return err
	}
	path := out
	if dir != "." {
		path = dir + string(os.PathSeparator) + out
	}
	if check {
		cur, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if !bytes.Equal(cur, src) {
			return fmt.Errorf("%s is out of date: run go generate", path)
		}
		return nil
	}
	if src == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, src, 0o644)
}
