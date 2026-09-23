package secret

import "context"

// NewFallback returns a Provider for a staged, reference-by-reference secret
// cutover: it resolves through primary and, only when primary reports
// ErrNotFound for a well-formed reference, asks secondary instead. It exists
// for a consumer migrating from one store to another (for example the
// built-in FileProvider to an external provider such as secret/foostore)
// without moving every reference at once:
//
//	api.SetSecretProvider(secret.NewSnapshot(secret.NewFallback(newProvider, secret.FileProvider{})))
//
// A reference newProvider's own lookup table does not (yet) list is its
// ErrNotFound (see foostore.Provider.Resolve, "an unmapped one is
// ErrNotFound"), so Fallback reads it from secondary unchanged, exactly as
// before the cutover began. Adding a reference to newProvider's table is the
// whole migration step for that one secret; every other reference, and the
// logical Ref strings recipes pass to MustSecret/OptionalSecret, stay
// exactly as they are.
//
// The one property that makes this safe during a cutover: every failure from
// primary OTHER than ErrNotFound — ErrInvalid, ErrUnreadable, ErrUnavailable,
// a cancelled context, or an unclassified error Resolve has already turned
// into ErrUnavailable — is returned exactly as primary reported it, and
// secondary is never consulted. A primary that is locked, unauthenticated,
// corrupt or otherwise broken for a reference it DOES map must fail loudly;
// silently falling back to secondary's possibly stale copy of the same
// secret would make that failure indistinguishable from an ordinary
// not-yet-migrated reference, and every caller (MustSecret, OptionalSecret,
// a recipe body) would keep working on stale bytes without anyone noticing
// the new store had stopped answering. A Fallback that instead fell back on
// every error would be exactly that bug; see secret/fallback_test.go's
// TestFallbackDoesNotConsultSecondaryOnNonNotFound, which pins this by
// asserting secondary is never even called.
//
// Fallback holds no state of its own and puts no secret bytes into any error,
// log line or panic value; it is safe for concurrent use whenever primary and
// secondary are. Wrap the whole composition in NewSnapshot as usual — caching
// applies to Fallback's combined result, not to primary and secondary
// individually, so a reference resolved via secondary is cached exactly like
// one primary answered directly.
func NewFallback(primary, secondary Provider) Provider {
	return &fallbackProvider{primary: primary, secondary: secondary}
}

// fallbackProvider is the unexported Provider NewFallback returns.
type fallbackProvider struct {
	primary, secondary Provider
}

// Resolve implements Provider. It calls Resolve (the package function, not
// f.primary.Resolve directly) so primary's failure is classified exactly as
// api.ResolveSecret or a Snapshot would see it, including a caller ctx that
// becomes done during primary's call; that classified result is what decides
// whether secondary runs.
func (f *fallbackProvider) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	data, err := Resolve(ctx, f.primary, ref)
	if err == nil {
		return data, nil
	}
	if !IsNotFound(err) {
		return nil, err
	}
	return Resolve(ctx, f.secondary, ref)
}
