# 5. Templates

gonf renders Go `text/template` templates in two places: on the
destination while applying, or on the controller while recording. Pick the
destination when the output depends on the host, and the controller when it
only depends on your data.

> 🦫 **Gonfy says:** You can carve a sign at the lodge, with the lodge's own facts, or at home before the trip, with only what you brought along. Both are templates; they differ in where they are carved.

## The recipe

```go
// Command recipe renders templates (tutorial chapter 5).
package main

import (
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

	// 2. RenderTemplate renders on the controller, while the recipe runs.
	//    Only your data is available, and the result is plain content.
	//    WithContentFrom takes its (string, error) result directly: a render
	//    error refuses the file.
	File(dir+"/banner.txt",
		WithContentFrom(RenderTemplate("assets/templates/banner.tmpl", site)), WithMode(0o644))
}
```

The two templates:

```text
# {{ .Param }} rendered on {{ .Gonf.Hostname }} ({{ .Gonf.GOOS }}, profile {{ .Gonf.Profile }})
server {{ .Name }} {
  listen {{ .Port }}
  backends {{ join .Backend ", " }}
}
```

```text
*** {{ upper .Name }} ***
```

## Run it

> 🦫 **Gonfy says:** Look at the banner: my name, in capitals, carved at home before the trip.

```text
$ ./gonf templates
2026/09/26 08:23:13 created directory /home/paul/gonf-tutorial/templates
2026/09/26 08:23:13 updated /home/paul/gonf-tutorial/templates/site.conf
2026/09/26 08:23:13 updated /home/paul/gonf-tutorial/templates/banner.txt
summary: 0 ok, 3 changed, 0 skipped, 0 would-change
  changed Directory[/home/paul/gonf-tutorial/templates]
  changed File[/home/paul/gonf-tutorial/templates/site.conf]
  changed File[/home/paul/gonf-tutorial/templates/banner.txt]
$ cat /home/paul/gonf-tutorial/templates/site.conf
# assets/templates/site.conf.tmpl rendered on vm (linux, profile ubuntu)
server gonfy {
  listen 8080
  backends 10.0.0.1, 10.0.0.2
}
$ cat /home/paul/gonf-tutorial/templates/banner.txt
*** GONFY ***
```

`site.conf` was rendered on the destination: `.Gonf.Hostname`, `.Gonf.GOOS`
and `.Gonf.Profile` are that host's facts, and `.Param` is the declared
source path. `-profile` overrides the detected profile for a local run:

```text
$ ./gonf -profile fedora templates
2026/09/26 08:23:13 updated /home/paul/gonf-tutorial/templates/site.conf
summary: 2 ok, 1 changed, 0 skipped, 0 would-change
  changed File[/home/paul/gonf-tutorial/templates/site.conf]
$ head -1 /home/paul/gonf-tutorial/templates/site.conf
# assets/templates/site.conf.tmpl rendered on vm (linux, profile fedora)
```

## What the plan carries

A destination template travels as the template plus its data, not as the
rendered text. A controller template is plain content by the time it is
recorded:

```text
$ ./gonf plan -redacted templates
{"op":"plan_preview","version":21,"id":"plan"}
{"op":"dir","id":"Directory[${HOME}/gonf-tutorial/templates]","path":"${HOME}/gonf-tutorial/templates","mode":"0755"}
{"op":"file","id":"File[${HOME}/gonf-tutorial/templates/site.conf]","path":"${HOME}/gonf-tutorial/templates/site.conf","mode":"0644","content_b64":"IyB7eyAuUGFyYW0gfX0gcmVuZGVyZWQgb24ge3sgLkdvbmYuSG9zdG5hbWUgfX0gKHt7IC5Hb25mLkdPT1MgfX0sIHByb2ZpbGUge3sgLkdvbmYuUHJvZmlsZSB9fSkKc2VydmVyIHt7IC5OYW1lIH19IHsKICBsaXN0ZW4ge3sgLlBvcnQgfX0KICBiYWNrZW5kcyB7eyBqb2luIC5CYWNrZW5kICIsICIgfX0KfQo=","has_content":true,"template":true,"template_param":"assets/templates/site.conf.tmpl","template_data":{"Name":"gonfy","Port":8080,"Backend":["10.0.0.1","10.0.0.2"]}}
{"op":"file","id":"File[${HOME}/gonf-tutorial/templates/banner.txt]","path":"${HOME}/gonf-tutorial/templates/banner.txt","mode":"0644","content_b64":"KioqIEdPTkZZICoqKgo=","has_content":true}
wrote redacted preview to stdout (4 ops, 0 secret-bearing; not a plan, cannot be applied)
```

(`plan -redacted` prints a plan for reading, chapter 11.) The first file op
has `"template":true` and your `template_data`. The second has only
`content_b64`, the base64 of `*** GONFY ***`.

## Which one to use

> 🦫 **Gonfy says:** Host names and OS differences are carved at the lodge. Anything from your own data can be carved at home.

| | Destination (`.tmpl` source, `WithTemplate`, `WithTemplateData`) | Controller (`RenderTemplate`) |
|--|--|--|
| Renders | while applying | while recording |
| Data | your data, `.Gonf` facts, environment, `.Param` | your data only |
| Result in the plan | template and data | plain content |
| Use for | host names, OS differences | files built from recipe data, secrets |

Both have the helpers `join`, `lower`, `upper`, `trim` and `replace`, and
both fail on an unknown key instead of printing `<no value>`.
`WithContentFrom(RenderTemplate(...))` takes the `(string, error)` pair
directly, so a render error refuses the file instead of writing an empty
one.

Reference: [Templates](../reference.md#templates),
[File](../reference.md#file), [Facts](../reference.md#facts).

---

← [4. Editing and validating files](04-editing-files.md) · [Contents](README.md) · Next: [6. Commands and change gates](06-commands.md) →
