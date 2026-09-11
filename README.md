# gonf

<img src="assets/logo-light.svg" alt="Gonf logo" width="140">

Pure-Go syntax KISS configuration management system for personal use. Built with the help of AI, but designed, reviewed and tested by a human.

## Quick notes

- `List(...)` builds a `[]string` for multi-path resources, `Command` args, and `EachKV` pairs (formerly `Elems`).
- Task registration: `Task`, `RegisterMethods`, `Aggregate`, `CLI`.
- Path helpers: `Home`, `Expand`, `SyncDir`, `InstallFile`, `SymlinkMap`, …
