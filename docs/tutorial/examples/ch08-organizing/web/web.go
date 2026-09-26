// Package web holds the website tasks of the tutorial's chapter 8 recipe.
package web

//go:generate go run github.com/snonux/gonf/cmd/gonf-desc

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
)

// Web is registered with RegisterMethods: every exported method is a task,
// named web_<method> in snake_case. The -list descriptions come from the
// doc comments below, via gonf-desc (see desc_gen.go).
type Web struct{}

var root = DestHome("gonf-tutorial/site")

// Docroot creates the document root.
func (Web) Docroot() { Dir(root+"/htdocs", WithMode(0o755)) }

// Config writes the web server config.
func (Web) Config() {
	File(root+"/site.conf", WithContent("docroot htdocs\n"), WithMode(0o644))
}

// OptsConfig adds task options: the document root is created first.
// Needs takes the method itself, so an editor can jump to it.
func (Web) OptsConfig() TaskOptions { return TaskOptions{Needs(Web.Docroot)} }

// Content publishes the start page.
func (Web) Content() {
	File(root+"/htdocs/index.html", WithContent("<h1>Hello</h1>\n"), WithMode(0o644))
}

// OptsContent adds task options: the document root is created first.
func (Web) OptsContent() TaskOptions { return TaskOptions{Needs(Web.Docroot)} }

// Logrotate rotates the web server logs (OpenBSD only).
func (Web) Logrotate() {
	File(root+"/newsyslog.conf", WithLines("access.log 644 7 * @T00 Z"), WithMode(0o644))
}

// WhenLogrotate guards the Logrotate task: it only applies on OpenBSD.
func (Web) WhenLogrotate() TaskOption { return WhenOpenBSD() }
