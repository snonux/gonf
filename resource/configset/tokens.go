package configset

import (
	"bytes"
	"fmt"

	opt "github.com/snonux/gonf/resource/options"
)

// tokenMarker is the common start of both member-path token kinds. Any
// occurrence of it that is not a complete, well-formed token is an error, so
// a truncated or misspelt placeholder can never reach a live file verbatim.
const tokenMarker = "\x00gonf-member-"

// tokenRef is one parsed member-path token.
type tokenRef struct {
	key    string
	chroot bool // MemberChrootPath rather than MemberPath
}

// resolver maps a parsed token to the path text it renders to. The staged
// resolver returns candidate paths inside the private staging directory, the
// live resolver the members' real paths.
type resolver func(ref tokenRef) (string, error)

// render replaces every member-path token in content through resolve. The
// rest of the bytes are copied verbatim: this is the only transformation the
// destination applies to controller-rendered member content, so staged and
// live bytes differ exactly in the member paths and nowhere else.
func render(content []byte, resolve resolver) ([]byte, error) {
	var out bytes.Buffer
	rest := content
	for {
		i := bytes.Index(rest, []byte(tokenMarker))
		if i < 0 {
			out.Write(rest)
			return out.Bytes(), nil
		}
		out.Write(rest[:i])
		ref, n, err := parseToken(rest[i:])
		if err != nil {
			return nil, err
		}
		path, err := resolve(ref)
		if err != nil {
			return nil, err
		}
		out.WriteString(path)
		rest = rest[i+n:]
	}
}

// renderArgs is render for a validator argv. Tokens may appear anywhere in an
// argument (e.g. "-c" + MemberPath("x") as one element); each element is
// rendered independently and the caller's slice is never modified.
func renderArgs(args []string, resolve resolver) ([]string, error) {
	out := make([]string, len(args))
	for i, arg := range args {
		rendered, err := render([]byte(arg), resolve)
		if err != nil {
			return nil, fmt.Errorf("argument %d: %w", i+1, err)
		}
		out[i] = string(rendered)
	}
	return out, nil
}

// parseToken parses the token at the start of b and returns it together with
// its length in bytes.
func parseToken(b []byte) (tokenRef, int, error) {
	var ref tokenRef
	var prefix string
	switch {
	case bytes.HasPrefix(b, []byte(opt.MemberPathTokenPrefix)):
		prefix = opt.MemberPathTokenPrefix
	case bytes.HasPrefix(b, []byte(opt.MemberChrootPathTokenPrefix)):
		prefix, ref.chroot = opt.MemberChrootPathTokenPrefix, true
	default:
		return ref, 0, fmt.Errorf("malformed member-path placeholder (use MemberPath or MemberChrootPath)")
	}
	body := b[len(prefix):]
	end := bytes.Index(body, []byte(opt.MemberTokenSuffix))
	if end < 0 {
		return ref, 0, fmt.Errorf("unterminated member-path placeholder")
	}
	ref.key = string(body[:end])
	if err := checkKey(ref.key); err != nil {
		return ref, 0, fmt.Errorf("member-path placeholder: %w", err)
	}
	return ref, len(prefix) + end + len(opt.MemberTokenSuffix), nil
}

// references returns every token in content, failing on malformed ones. The
// spec validation uses it to reject unknown keys and chroot-relative tokens
// without WithChroot at record time, long before any apply.
func references(content []byte) ([]tokenRef, error) {
	var refs []tokenRef
	_, err := render(content, func(ref tokenRef) (string, error) {
		refs = append(refs, ref)
		return "", nil
	})
	return refs, err
}
