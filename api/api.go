package api

import (
	"codeberg.org/snonux/gonf/api/options"
	"codeberg.org/snonux/gonf/resource"
	"codeberg.org/snonux/gonf/resource/cmd"
	"codeberg.org/snonux/gonf/resource/dir"
	"codeberg.org/snonux/gonf/resource/file"
	"codeberg.org/snonux/gonf/resource/link"
	"codeberg.org/snonux/gonf/resource/pkg"
)

// Path constraint for resources that can be defined as a single item or a list.
type Path interface {
	string | []string
}

// File creates one or more file resources.
func File[T Path](path T, opts ...options.Option) Resource {
	switch v := any(path).(type) {
	case string:
		return file.Present(v, opts...)
	case []string:
		return Files(v, opts...)
	default:
		panic("File: path must be string or []string")
	}
}

func Files(paths []string, opts ...options.Option) Resource {
	var resources []resource.Resource
	for _, path := range paths {
		resources = append(resources, file.Present(path, opts...))
	}
	return resource.Multi(resources)
}

// NoFile creates one or more file resources that are ensured to be absent.
func NoFile[T Path](path T, opts ...options.Option) Resource {
	return File(path, append(opts, options.IsAbsent)...)
}

// Dir creates one or more directory resources.
func Dir[T Path](path T, opts ...options.Option) Resource {
	switch v := any(path).(type) {
	case string:
		return dir.Present(v, opts...)
	case []string:
		return Dirs(v, opts...)
	default:
		panic("Dir: path must be string or []string")
	}
}

func Dirs(paths []string, opts ...options.Option) Resource {
	var resources []resource.Resource
	for _, path := range paths {
		resources = append(resources, dir.Present(path, opts...))
	}
	return resource.Multi(resources)
}

// NoDir creates one or more directory resources that are ensured to be absent.
func NoDir[T Path](path T, opts ...options.Option) Resource {
	return Dir(path, append(opts, options.IsAbsent)...)
}

// Link creates one or more link resources (symbolic or hard).
func Link[T Path](path T, opts ...options.Option) Resource {
	switch v := any(path).(type) {
	case string:
		return link.Present(v, opts...)
	case []string:
		return Links(v, opts...)
	default:
		panic("Link: path must be string or []string")
	}
}

func Links(paths []string, opts ...options.Option) Resource {
	var resources []resource.Resource
	for _, path := range paths {
		resources = append(resources, link.Present(path, opts...))
	}
	return resource.Multi(resources)
}

// NoLink creates one or more link resources that are ensured to be absent.
func NoLink[T Path](path T, opts ...options.Option) Resource {
	return Link(path, append(opts, options.IsAbsent)...)
}

// Elems is a helper to create a slice of strings from variadic arguments.
func Elems(paths ...string) []string {
	return paths
}

// Package creates one or more package resources.
func Package[T Path](name T, opts ...options.Option) Resource {
	switch v := any(name).(type) {
	case string:
		return pkg.Present(v, opts...)
	case []string:
		return Packages(v, opts...)
	default:
		panic("Package: name must be string or []string")
	}
}

func Packages(names []string, opts ...options.Option) Resource {
	var resources []resource.Resource
	for _, name := range names {
		resources = append(resources, pkg.Present(name, opts...))
	}
	return resource.Multi(resources)
}

// NoPackage creates one or more package resources that are ensured to be absent.
func NoPackage[T Path](name T, opts ...options.Option) Resource {
	return Package(name, append(opts, options.IsAbsent)...)
}

// Command registers a command resource that runs name with args on Apply.
// Use options.Unless, options.OnlyIf, or options.Creates for idempotency.
func Command(name string, args []string, opts ...options.Option) Resource {
	return cmd.Present(name, args, opts...)
}
