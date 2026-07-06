package api

import (
	"codeberg.org/snonux/gonf/api/options"
	"codeberg.org/snonux/gonf/internal/resource/dir"
	"codeberg.org/snonux/gonf/internal/resource/file"
	"codeberg.org/snonux/gonf/internal/resource/link"
)

// File creates a file resource.
func File(path string, opts ...options.Option) Resource {
	return file.Present(path, opts...)
}

// NoFile creates a file resource that is ensured to be absent.
func NoFile(path string, opts ...options.Option) Resource {
	return file.Absent(path, opts...)
}

// Dir creates a directory resource.
func Dir(path string, opts ...options.Option) Resource {
	return dir.Present(path, opts...)
}

// NoDir creates a directory resource that is ensured to be absent.
func NoDir(path string, opts ...options.Option) Resource {
	return dir.Absent(path, opts...)
}

// Link creates a link resource (symbolic or hard).
func Link(path string, opts ...options.Option) Resource {
	return link.Present(path, opts...)
}

// NoLink creates a link resource that is ensured to be absent.
func NoLink(path string, opts ...options.Option) Resource {
	return link.Absent(path, opts...)
}
