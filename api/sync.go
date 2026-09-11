package api

import (
	"github.com/snonux/gonf/api/options"
)

// SyncDir installs files matching srcGlob into dst as a Dir resource.
// Defaults: directory mode 0700, file mode 0640. Extra opts are appended
// after the defaults (later WithMode / WithFileMode / WithPrune win).
func SyncDir(dst, srcGlob string, opts ...options.Option) Resource {
	base := []options.Option{
		options.WithSourceGlob(Expand(srcGlob)),
		options.WithMode(0o700),
		options.WithFileMode(0o640),
	}
	return Dir(Expand(dst), append(base, opts...)...)
}

// InstallFile copies src onto dst as a File resource.
// Default mode is 0640; extra opts are appended after the defaults.
func InstallFile(dst, src string, opts ...options.Option) Resource {
	base := []options.Option{
		options.WithSource(Expand(src)),
		options.WithMode(0o640),
	}
	return File(Expand(dst), append(base, opts...)...)
}
