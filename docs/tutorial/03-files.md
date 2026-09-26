# 3. Files, directories and links

Most configuration is files. This chapter builds a small tree below
`~/gonf-tutorial`: inline content, copied files, a synced directory tree, a
glob install, links, files created only once, and a file that must be gone.

> 🦫 **Gonfy says:** I carry sticks from the riverbank to the lodge. The riverbank is the controller, where `Home` and `WithSource` read. The lodge is the destination, where `DestHome` points.

## The recipe

```go
// Command gonf manages files, directories and links (tutorial chapter 3).
package main

import (
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
```

The source paths (`assets/...`) are relative to the directory you run
`./gonf` from, here `docs/tutorial/examples`:

```text
assets/
├── bin/            disk, load
├── gonfy.txt       Gonfy's portrait
└── dotfiles/
    ├── bashrc
    └── vim/        vimrc, colors/tutorial.vim
```

## Sources live on the controller, targets on the destination

`WithSource("assets/dotfiles/bashrc")` is read **while recording**, on the
controller, and its bytes go into the plan. The target path is used **while
applying**, on the destination. That is why targets use `DestHome`:

| Helper | Expands to | Use it for |
|--------|------------|------------|
| `DestHome("x")` | `${HOME}/x`, expanded on the destination when the plan applies | targets |
| `Home("x")` | the controller's `$HOME/x`, right now | sources |

For a local run both are the same directory. They differ once you push to
another host (chapter 12), so get the habit now.

Reference: [Body-level guards and helpers](../reference.md#body-level-guards-and-helpers).

## Run it

> 🦫 **Gonfy says:** Always look before you gnaw: `-n` shows every change and makes none.

Build your gonf and preview it with `-n` on a fresh home first:

```text
$ go build -o gonf ./ch03-files
$ ./gonf -list
cleanup	Remove what the files task created
files	Files, directories and links under ~/gonf-tutorial
$ ./gonf -n files
2026/09/26 09:23:54 dry-run: would create directory /home/paul/gonf-tutorial
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/motd
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/bashrc
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/gonfy.txt
2026/09/26 09:23:54 dry-run: would create directory /home/paul/gonf-tutorial/vim
2026/09/26 09:23:54 dry-run: would create directory /home/paul/gonf-tutorial/vim/colors
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/vim/colors/tutorial.vim
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/vim/vimrc
2026/09/26 09:23:54 dry-run: would create directory /home/paul/gonf-tutorial/bin
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/bin/disk
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/bin/load
2026/09/26 09:23:54 dry-run: symlink /home/paul/gonf-tutorial/vimrc target "/home/paul/gonf-tutorial/vim/vimrc" does not exist yet; the apply refuses it unless an earlier resource creates it
2026/09/26 09:23:54 dry-run: would create symlink /home/paul/gonf-tutorial/vimrc -> /home/paul/gonf-tutorial/vim/vimrc
2026/09/26 09:23:54 dry-run: would create directory /home/paul/gonf-tutorial/state
2026/09/26 09:23:54 dry-run: would update /home/paul/gonf-tutorial/state/notes.txt
summary: 2 ok, 0 changed, 0 skipped, 14 would-change
  would-change Directory[/home/paul/gonf-tutorial]
  would-change File[/home/paul/gonf-tutorial/motd]
  would-change File[/home/paul/gonf-tutorial/bashrc]
  would-change File[/home/paul/gonf-tutorial/gonfy.txt]
  would-change Directory[/home/paul/gonf-tutorial/vim]
  would-change Directory[/home/paul/gonf-tutorial/vim/colors]
  would-change File[/home/paul/gonf-tutorial/vim/colors/tutorial.vim]
  would-change File[/home/paul/gonf-tutorial/vim/vimrc]
  would-change Directory[/home/paul/gonf-tutorial/bin]
  would-change File[/home/paul/gonf-tutorial/bin/disk]
  would-change File[/home/paul/gonf-tutorial/bin/load]
  would-change Symlink[/home/paul/gonf-tutorial/vimrc]
  would-change Directory[/home/paul/gonf-tutorial/state]
  would-change EnsureFile[/home/paul/gonf-tutorial/state/notes.txt]
```

The dry run changes nothing, so the `vimrc` link's target `vim/vimrc` does
not exist yet when the link is checked. gonf notes that an earlier resource
of the same run may create it and previews the link as `would-change`. The
real apply still refuses a symlink whose target is missing when it gets to
it, so a typo in a target fails the run instead of leaving a dangling link.

Now apply it:

```text
$ ./gonf files
2026/09/26 09:23:54 created directory /home/paul/gonf-tutorial
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/motd
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/bashrc
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/gonfy.txt
2026/09/26 09:23:54 created directory /home/paul/gonf-tutorial/vim
2026/09/26 09:23:54 created directory /home/paul/gonf-tutorial/vim/colors
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/vim/colors/tutorial.vim
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/vim/vimrc
2026/09/26 09:23:54 created directory /home/paul/gonf-tutorial/bin
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/bin/disk
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/bin/load
2026/09/26 09:23:54 created symlink /home/paul/gonf-tutorial/vimrc -> /home/paul/gonf-tutorial/vim/vimrc
2026/09/26 09:23:54 created directory /home/paul/gonf-tutorial/state
2026/09/26 09:23:54 updated /home/paul/gonf-tutorial/state/notes.txt
summary: 2 ok, 14 changed, 0 skipped, 0 would-change
  changed Directory[/home/paul/gonf-tutorial]
  changed File[/home/paul/gonf-tutorial/motd]
  changed File[/home/paul/gonf-tutorial/bashrc]
  changed File[/home/paul/gonf-tutorial/gonfy.txt]
  changed Directory[/home/paul/gonf-tutorial/vim]
  changed Directory[/home/paul/gonf-tutorial/vim/colors]
  changed File[/home/paul/gonf-tutorial/vim/colors/tutorial.vim]
  changed File[/home/paul/gonf-tutorial/vim/vimrc]
  changed Directory[/home/paul/gonf-tutorial/bin]
  changed File[/home/paul/gonf-tutorial/bin/disk]
  changed File[/home/paul/gonf-tutorial/bin/load]
  changed Symlink[/home/paul/gonf-tutorial/vimrc]
  changed Directory[/home/paul/gonf-tutorial/state]
  changed EnsureFile[/home/paul/gonf-tutorial/state/notes.txt]
```

`Dir`, `SyncDir` and `EnsureDir` are parents of the files inside them, so
gonf creates them first without a `DependsOn`. Run it again and everything
is `ok`:

```text
$ ./gonf files
summary: 16 ok, 0 changed, 0 skipped, 0 would-change
$ find ~/gonf-tutorial | sort
/home/paul/gonf-tutorial
/home/paul/gonf-tutorial/bashrc
/home/paul/gonf-tutorial/bin
/home/paul/gonf-tutorial/bin/disk
/home/paul/gonf-tutorial/bin/load
/home/paul/gonf-tutorial/gonfy.txt
/home/paul/gonf-tutorial/motd
/home/paul/gonf-tutorial/state
/home/paul/gonf-tutorial/state/notes.txt
/home/paul/gonf-tutorial/vim
/home/paul/gonf-tutorial/vim/colors
/home/paul/gonf-tutorial/vim/colors/tutorial.vim
/home/paul/gonf-tutorial/vim/vimrc
/home/paul/gonf-tutorial/vimrc
$ readlink ~/gonf-tutorial/vimrc
/home/paul/gonf-tutorial/vim/vimrc
$ cat ~/gonf-tutorial/gonfy.txt
     __         __
    /  \.-"""-./  \
    \    -   -    /
     |   o   o   |      Hi, I'm Gonfy the beaver!
     \  .-'''-.  /      I keep this lodge converged.
      '-\__Y__/-'
          [||]
```

## Drift: pruning, removal and repair

> 🦫 **Gonfy says:** Twigs in the wrong room get swept out, and the stick that must not be there gets pulled.

Drop some twigs into the synced tree as a stray file, recreate the file that must be absent,
and loosen a mode:

```text
$ echo twigs > ~/gonf-tutorial/vim/stray.txt; echo old > ~/gonf-tutorial/old.conf; chmod 600 ~/gonf-tutorial/motd
$ ./gonf -n files
2026/09/26 09:23:54 dry-run: would prune /home/paul/gonf-tutorial/vim/stray.txt
2026/09/26 09:23:54 dry-run: would remove /home/paul/gonf-tutorial/old.conf
summary: 15 ok, 0 changed, 0 skipped, 2 would-change
  would-change File[/home/paul/gonf-tutorial/vim/stray.txt]
  would-change File[/home/paul/gonf-tutorial/old.conf]
$ stat -c '%a %n' /home/paul/gonf-tutorial/motd
600 /home/paul/gonf-tutorial/motd
$ ./gonf files
2026/09/26 09:23:54 pruned /home/paul/gonf-tutorial/vim/stray.txt
2026/09/26 09:23:54 removed /home/paul/gonf-tutorial/old.conf
summary: 15 ok, 2 changed, 0 skipped, 0 would-change
  changed File[/home/paul/gonf-tutorial/vim/stray.txt]
  changed File[/home/paul/gonf-tutorial/old.conf]
$ stat -c '%a %n' /home/paul/gonf-tutorial/motd
644 /home/paul/gonf-tutorial/motd
```

- `WithPrune` on the tree sync removes `stray.txt`: the destination mirrors
  the source.
- `NoFile` removes `old.conf`.
- The wrong mode on `motd` is repaired back to `0644`. A metadata-only
  repair counts as `ok`, not `changed`, so it does not fire change gates
  (chapter 6).

`cleanup` removes the whole tree:

```text
$ ./gonf cleanup
2026/09/26 09:23:54 removed /home/paul/gonf-tutorial
summary: 0 ok, 1 changed, 0 skipped, 0 would-change
  changed Directory[/home/paul/gonf-tutorial]
```

## The resources in this chapter

| Call | What it converges |
|------|-------------------|
| `File(path, WithContent(s))` | a file with exactly this content |
| `File(path, WithSource(p))` | a copy of a controller file |
| `Dir(path)` | a directory |
| `Dir(path, WithSource(dir), WithPrune)` | a mirrored tree, extra entries removed |
| `SyncDir(dst, glob)` | every file matching the glob, installed by basename |
| `Symlink(path, target)` | a symlink (short for `Link(path, WithSymlink(target))`) |
| `LinkIfExists(path, target)` | a symlink only while the target exists, otherwise no link |
| `EnsureDir`, `EnsureFile` | created when missing, never overwritten |
| `NoFile`, `NoDir`, `NoLink` | the path is gone |
| `WithMode`, `WithOwner`, `Perm(mode, owner)` | permissions (default file mode `0640`, directory `0750`) |

Two defaults worth knowing: a `File` without `WithMode` still gets `0640`,
and a `List(...)` path declares several files with the same options at once
(`File(List("/a", "/b"), ...)`).

Reference: [Resources](../reference.md#resources),
[Shared options](../reference.md#shared-options),
[File](../reference.md#file), [Dir](../reference.md#dir),
[Link](../reference.md#link).

---

← [2. Your first recipe](02-first-recipe.md) · [Contents](README.md) · Next: [4. Editing and validating files](04-editing-files.md) →
