// Command gonf manages files, directories and links (tutorial chapter 3).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("files", "Files, directories and links under ~/gonf-tutorial", files)
	Task("toolbox", "More file helpers under ~/gonf-tutorial/toolbox", toolbox)
	Task("git", "Gonfy's global git settings", git)
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

func toolbox() {
	box := DestHome("gonf-tutorial/toolbox")
	Dir(box, WithMode(0o755))

	// InstallFile is File with WithSource, and the default mode 0640.
	InstallFile(box+"/gonfy.txt", "assets/gonfy.txt", WithMode(0o644))

	// Dirs, Files and Links declare several paths with the same options.
	Dirs(List(box+"/logs", box+"/cache"), WithMode(0o755))

	// SyncDir spelled as Dir: the same glob sync, with Dir's defaults.
	Dir(box+"/scripts", WithSourceGlob("assets/bin/*"), WithFileMode(0o755))

	// One LinkIfExists per name and target pair, below one directory.
	SymlinkMap(box, "portrait", box+"/gonfy.txt", "gitconfig", DestHome(".gitconfig"))
}

func git() {
	// One "git config --global" command per key and value pair, skipped
	// when git already has that value.
	GitGlobal("user.name", "Gonfy", "init.defaultBranch", "main")
}

func cleanup() {
	NoDir(DestHome("gonf-tutorial"), WithPrune)
}
