// Command gonf manages files, directories and links (tutorial chapter 3).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("files", "Files, directories and links under ~/gonf-tutorial", files)
	Task("cleanup", "Remove what the files task created", cleanup)
	cli.Main()
}

func files() {
	base := DestHome("gonf-tutorial")

	// A directory, then a file with inline content inside it. gonf orders
	// the file after its parent directory on its own.
	Dir(base, WithMode(0o755))
	File(base+"/motd", WithContent("Welcome to Gonfy's lodge!\n"), WithMode(0o644))

	// Copy a file from the recipe's working directory (the controller).
	File(base+"/bashrc", WithSource("assets/dotfiles/bashrc"), WithMode(0o644))
	File(base+"/gonfy.txt", WithSource("assets/gonfy.txt"), WithMode(0o644))

	// Mirror a whole directory tree, removing anything not in the source.
	Dir(base+"/vim", WithSource("assets/dotfiles/vim"), WithPrune, WithFileMode(0o644))

	// Install every file matching a glob, by basename.
	SyncDir(base+"/bin", "assets/bin/*", WithMode(0o755), WithFileMode(0o755))

	// Links: a symlink, and one that only exists when its target does.
	Symlink(base+"/vimrc", base+"/vim/vimrc")
	LinkIfExists(base+"/gitconfig", DestHome(".gitconfig"))

	// Create once, never overwrite: good for files a program edits later.
	EnsureDir(base+"/state", WithMode(0o700))
	EnsureFile(base+"/state/notes.txt", WithMode(0o600))

	// Make sure something is gone.
	NoFile(base + "/old.conf")
}

func cleanup() {
	NoDir(DestHome("gonf-tutorial"), WithPrune)
}
