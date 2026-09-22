// Package foostore is a secret.Provider that reads secrets from a foostore
// KeePass store by executing the foostore binary with argv — it never imports
// foostore's internal Go packages.
//
// It speaks foostore's machine-facing read contract (foostore commit
// cd8de3d, gonf task y52; see foostore's README "Machine-Facing Read"),
// which this package calls contract version 1:
//
//	foostore read --backend keepass --exact --raw --non-interactive \
//	    --timeout D [--kdbx-path P] [--field NAME] -- REFERENCE
//
// Stdout carries exactly the requested bytes; the exit code classifies every
// failure (0 ok, 1 unexpected/timeout, 2 usage, 4 not found, 5 ambiguous,
// 6 locked, 7 corrupt, 8 store I/O). Before its first read a Provider checks
// that the binary implements that contract (`foostore read --help` must
// print the contract's usage), so an older foostore, whose `read` would be
// an interactive search, is never mistaken for a machine read.
//
// argv carries only logical references: the foostore entry or attachment
// reference, the field name, the store path and the timeout. The store
// passphrase never travels in argv or the environment; foostore resolves it
// itself (its configured kdbx_pass_file), or Config.Passphrase supplies it
// through an inherited pipe (FOOSTORE_READ_PASSPHRASE_FD).
//
// Configure it once at the consumer's composition root, wrapped in a
// secret.Snapshot so each reference is read at most once per gonf
// invocation (every task, host and chunk of one plan sees the same bytes):
//
//	items, err := foostore.Items(map[secret.Ref]foostore.Item{
//	    "garage/rpc_secret": foostore.Field("Infra/garage-rpc", "Password"),
//	    "nsd/tsig.key":      foostore.Attachment("Infra/nsd/tsig.key"),
//	})
//	...
//	p, err := foostore.New(foostore.Config{Lookup: items})
//	...
//	api.SetSecretProvider(secret.NewSnapshot(p))
package foostore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/snonux/gonf/secret"
)

// DefaultBinary is the foostore executable looked up in PATH when
// Config.Binary is empty.
const DefaultBinary = "foostore"

// DefaultTimeout bounds one read when Config.Timeout is zero; it matches
// foostore's own --timeout default.
const DefaultTimeout = 30 * time.Second

// DefaultMaxBytes bounds the stdout accepted for one secret when
// Config.MaxBytes is zero.
const DefaultMaxBytes = 16 << 20

// killGrace is how much longer than the timeout passed to foostore the
// adapter waits before killing the process itself: foostore normally stops
// on its own --timeout (exit 1), and the adapter's kill is the backstop for
// a binary that hangs anyway.
const killGrace = 2 * time.Second

// probeTimeout bounds the contract check (`read --help`, which needs no
// store, no config and no KDF).
const probeTimeout = 10 * time.Second

// probeMaxBytes bounds the usage text the contract check accepts; it is
// independent of Config.MaxBytes, which bounds secrets.
const probeMaxBytes = 64 << 10

var _ secret.Provider = (*Provider)(nil)

// Config configures a Provider. Only Lookup is required.
type Config struct {
	// Binary is the foostore executable: a path, or a name looked up in
	// PATH (DefaultBinary when empty).
	Binary string
	// KDBXPath, when set, is passed as --kdbx-path; otherwise foostore uses
	// its configured store.
	KDBXPath string
	// Timeout bounds one read (DefaultTimeout when zero). It is passed to
	// foostore as --timeout; the adapter kills the process group if it is
	// still running shortly after.
	Timeout time.Duration
	// MaxBytes bounds the size of one secret (DefaultMaxBytes when zero).
	// Larger output is discarded and refused as ErrInvalid.
	MaxBytes int
	// Lookup maps a gonf reference to the foostore item it names; false
	// means the store has no such secret (ErrNotFound, which OptionalSecret
	// suppresses). Items builds one from a table. It must be safe for
	// concurrent use.
	Lookup func(secret.Ref) (Item, bool)
	// Passphrase, when set, returns the store passphrase for one read; it is
	// written into a pipe inherited by foostore as FOOSTORE_READ_PASSPHRASE_FD
	// (the pipe is consumed by each read, so it is called once per read).
	// When nil, foostore unlocks with its own configured kdbx_pass_file.
	// Its error is reported as ErrUnavailable and must not carry secret
	// bytes. The returned slice is overwritten after use.
	Passphrase func(ctx context.Context) ([]byte, error)
}

// Item is one foostore secret: an entry field (Field set) or an attachment
// (Field empty). Reference is foostore's exact identity — "Group/Title" for
// an entry, "Group/Title/name" for an attachment — compared byte for byte by
// foostore (no trimming or path cleaning). Build it with Field or
// Attachment.
type Item struct {
	Reference string
	Field     string
}

// Provider resolves gonf secret references through the foostore binary. It
// is safe for concurrent use. Create it with New.
type Provider struct {
	cfg Config

	mu     sync.Mutex // guards probed
	probed bool       // the binary passed the contract check

	// Test seams (package tests only): prefix is inserted before the
	// foostore argv (to run the test binary as a fake foostore) and extraEnv
	// is appended to the child's environment.
	prefix   []string
	extraEnv []string
}

// New returns a Provider for cfg, with defaults applied. It refuses a
// configuration without Lookup or with a negative Timeout or MaxBytes.
func New(cfg Config) (*Provider, error) {
	switch {
	case cfg.Lookup == nil:
		return nil, errors.New("foostore: Config.Lookup is required")
	case cfg.Timeout < 0:
		return nil, fmt.Errorf("foostore: negative Config.Timeout %v", cfg.Timeout)
	case cfg.MaxBytes < 0:
		return nil, fmt.Errorf("foostore: negative Config.MaxBytes %d", cfg.MaxBytes)
	}
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	return &Provider{cfg: cfg}, nil
}

// Field returns the Item selecting field name (e.g. "Password",
// "UserName") of the foostore entry reference.
func Field(reference, name string) Item { return Item{Reference: reference, Field: name} }

// Attachment returns the Item selecting the foostore attachment reference
// ("Group/Title/name").
func Attachment(reference string) Item { return Item{Reference: reference} }

// Items returns a Lookup over table. Its keys and the references looked up
// are compared in canonical form (secret.CanonicalRef), so "/nsd/key" and
// "nsd/key" name one item, as they name one file for the file provider. It
// refuses a key without a canonical form, two keys with the same canonical
// form, and an item without a reference.
func Items(table map[secret.Ref]Item) (func(secret.Ref) (Item, bool), error) {
	canon := make(map[secret.Ref]Item, len(table))
	for ref, item := range table {
		key, ok := secret.CanonicalRef(ref)
		if !ok {
			return nil, fmt.Errorf("foostore: reference %q has no canonical form", string(ref))
		}
		if _, dup := canon[key]; dup {
			return nil, fmt.Errorf("foostore: references canonicalising to %q are mapped more than once", string(key))
		}
		if item.Reference == "" {
			return nil, fmt.Errorf("foostore: reference %q maps to an empty foostore reference", string(ref))
		}
		canon[key] = item
	}
	return func(ref secret.Ref) (Item, bool) {
		key, ok := secret.CanonicalRef(ref)
		if !ok {
			return Item{}, false
		}
		item, ok := canon[key]
		return item, ok
	}, nil
}

// Resolve implements secret.Provider: it reads the item ref maps to with
// `foostore read` and returns stdout exactly. Failures are *secret.Error
// values naming ref and, where it helps, the foostore reference and exit
// code — never foostore's stdout or stderr, which may hold secret bytes
// (only the stderr size is reported). A done ctx kills the foostore process
// group and yields an error wrapping ctx.Err().
func (p *Provider) Resolve(ctx context.Context, ref secret.Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("resolve secret %q: %w", string(ref), err)
	}
	item, ok := p.cfg.Lookup(ref)
	if !ok {
		return nil, &secret.Error{Kind: secret.ErrNotFound, Ref: ref,
			Err: errors.New("not mapped to a foostore item")}
	}
	if item.Reference == "" {
		return nil, &secret.Error{Kind: secret.ErrInvalid, Ref: ref,
			Err: errors.New("mapped to an empty foostore reference")}
	}
	if err := p.checkContract(ctx, ref); err != nil {
		return nil, err
	}
	return p.read(ctx, ref, item)
}

// read runs one `foostore read` for item and classifies its outcome.
func (p *Provider) read(ctx context.Context, ref secret.Ref, item Item) ([]byte, error) {
	pass, err := p.passphrase(ctx, ref)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout+killGrace)
	defer cancel()
	res := p.run(runCtx, p.readArgs(item), pass, p.cfg.MaxBytes)
	clear(pass)
	res.timedOut = runCtx.Err() != nil
	if err := ctx.Err(); err != nil {
		res.scrub()
		return nil, fmt.Errorf("resolve secret %q: %w", string(ref), err)
	}
	if err := classify(ref, item, res, p.cfg); err != nil {
		res.scrub()
		return nil, err
	}
	return res.stdout, nil
}

// readArgs is the contract-v1 argv for item (after the binary name).
// Everything in it is a logical reference or a setting, never a secret.
func (p *Provider) readArgs(item Item) []string {
	args := []string{"read", "--backend", "keepass", "--exact", "--raw", "--non-interactive",
		"--timeout", p.cfg.Timeout.String()}
	if p.cfg.KDBXPath != "" {
		args = append(args, "--kdbx-path", p.cfg.KDBXPath)
	}
	if item.Field != "" {
		args = append(args, "--field", item.Field)
	}
	return append(args, "--", item.Reference)
}

// passphrase returns the configured passphrase for one read, or nil when
// foostore unlocks on its own.
func (p *Provider) passphrase(ctx context.Context, ref secret.Ref) ([]byte, error) {
	if p.cfg.Passphrase == nil {
		return nil, nil
	}
	pass, err := p.cfg.Passphrase(ctx)
	if err != nil {
		return nil, &secret.Error{Kind: secret.ErrUnavailable, Ref: ref,
			Err: fmt.Errorf("foostore passphrase source: %w", err)}
	}
	if len(pass) == 0 {
		return nil, &secret.Error{Kind: secret.ErrUnavailable, Ref: ref,
			Err: errors.New("foostore passphrase source returned an empty passphrase")}
	}
	return pass, nil
}

// checkContract verifies once per Provider that the binary implements the
// machine read contract. A failed check is not remembered, so a binary
// installed or fixed later is picked up; a passed one is.
func (p *Provider) checkContract(ctx context.Context, ref secret.Ref) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.probed {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	res := p.run(probeCtx, []string{"read", "--help"}, nil, probeMaxBytes)
	res.timedOut = probeCtx.Err() != nil
	defer res.scrub()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("resolve secret %q: %w", string(ref), err)
	}
	if err := probeError(res, p.cfg.Binary); err != nil {
		return &secret.Error{Kind: secret.ErrUnavailable, Ref: ref, Err: err}
	}
	p.probed = true
	return nil
}

// childEnv is the whole environment of a foostore child: HOME (foostore
// reads ~/.config/foostore.json), the passphrase descriptor when one is
// passed, and the test seam. Nothing else is inherited — in particular no
// FOOSTORE_SHELL, PIN, inherited FOOSTORE_READ_PASSPHRASE_FD or terminal
// settings that could steer foostore towards an interactive path.
func (p *Provider) childEnv(withPassFD bool) []string {
	var env []string
	if home, ok := os.LookupEnv("HOME"); ok {
		env = append(env, "HOME="+home)
	}
	if withPassFD {
		env = append(env, passFDEnv+"="+passFD)
	}
	return append(env, p.extraEnv...)
}
