// Command gonf renders templates (tutorial chapter 5).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

// Site is the data the templates below render.
type Site struct {
	Name    string
	Port    int
	Backend []string
}

func main() {
	Task("templates", "Render templates into ~/gonf-tutorial/templates", templates)
	cli.Main()
}

func templates() {
	site := Site{Name: "gonfy", Port: 8080, Backend: []string{"10.0.0.1", "10.0.0.2"}}
	dir := DestHome("gonf-tutorial/templates")
	Dir(dir, WithMode(0o755))

	// 1. A .tmpl source renders on the destination, with the destination's
	//    facts (.Gonf) and your data (WithTemplateData).
	File(dir+"/site.conf", WithSource("assets/templates/site.conf.tmpl"),
		WithTemplateData(site), WithMode(0o644))

	// 2. RenderTemplate renders on the controller, while gonf records.
	//    Only your data is available, and the result is plain content.
	//    WithContentFrom takes its (string, error) result directly: a render
	//    error refuses the file.
	File(dir+"/banner.txt",
		WithContentFrom(RenderTemplate("assets/templates/banner.tmpl", site)), WithMode(0o644))

	// 3. Inline content is a destination template too once it has data.
	//    Data that is not a struct or a map, like this list, has no keys:
	//    reach it as .Data. WithParam replaces {{ .Param }}.
	File(dir+"/backends.txt",
		WithContent("# {{ .Param }}\n{{ range .Data }}backend {{ . }}\n{{ end }}"),
		WithTemplateData(site.Backend), WithParam("Gonfy's backends"), WithMode(0o644))
}
