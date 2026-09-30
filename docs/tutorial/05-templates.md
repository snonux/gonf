# 5. Templates

gonf renders Go `text/template` templates in two places: on the
destination while applying, or on the controller while recording. Pick the
destination when the output depends on the host, and the controller when it
only depends on your data.

> 🦫 **Gonfy says:** You can carve a sign at the lodge, with the lodge's own facts, or at home before the trip, with only what you brought along. Both are templates; they differ in where they are carved.

## The recipe

```go
// Command gonf renders templates (tutorial chapter 5).
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
2026/09/30 04:14:09 created directory /home/paul/gonf-tutorial/templates
2026/09/30 04:14:09 updated /home/paul/gonf-tutorial/templates/site.conf
2026/09/30 04:14:09 updated /home/paul/gonf-tutorial/templates/banner.txt
2026/09/30 04:14:09 updated /home/paul/gonf-tutorial/templates/backends.txt
summary: 0 ok, 4 changed, 0 skipped, 0 would-change
  changed Directory[/home/paul/gonf-tutorial/templates]
  changed File[/home/paul/gonf-tutorial/templates/site.conf]
  changed File[/home/paul/gonf-tutorial/templates/banner.txt]
  changed File[/home/paul/gonf-tutorial/templates/backends.txt]
$ cat /home/paul/gonf-tutorial/templates/site.conf
# assets/templates/site.conf.tmpl rendered on vm (linux, profile ubuntu)
server gonfy {
  listen 8080
  backends 10.0.0.1, 10.0.0.2
}
$ cat /home/paul/gonf-tutorial/templates/banner.txt
*** GONFY ***
$ cat /home/paul/gonf-tutorial/templates/backends.txt
# Gonfy's backends
backend 10.0.0.1
backend 10.0.0.2
```

`site.conf` was rendered on the destination: `.Gonf.Hostname`, `.Gonf.GOOS`
and `.Gonf.Profile` are that host's facts, and `.Param` is the declared
source path.

`backends.txt` shows two more things a template can read:

- `.Data` is your whole data value. The fields of a struct or the keys of a
  map are also at the top level (`.Name`), but a list has no fields, so
  the template ranges over `.Data`. Both paths have `.Data`.
- `WithParam(s)` replaces `.Param`, here with `Gonfy's backends`.
  Without it, `.Param` is the template's source path; for a `.tmpl` file
  in a synced tree (chapter 3), that is its path in the source tree.
`-profile` overrides the detected profile for a local run:

```text
$ ./gonf -profile fedora templates
2026/09/30 04:14:09 updated /home/paul/gonf-tutorial/templates/site.conf
summary: 3 ok, 1 changed, 0 skipped, 0 would-change
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
{"op":"file","id":"File[${HOME}/gonf-tutorial/templates/backends.txt]","path":"${HOME}/gonf-tutorial/templates/backends.txt","mode":"0644","content_b64":"IyB7eyAuUGFyYW0gfX0Ke3sgcmFuZ2UgLkRhdGEgfX1iYWNrZW5kIHt7IC4gfX0Ke3sgZW5kIH19","has_content":true,"template":true,"template_param":"Gonfy's backends","template_data":["10.0.0.1","10.0.0.2"]}
wrote redacted preview to stdout (5 ops, 0 secret-bearing; not a plan, cannot be applied)
```

The first file op has `"template":true` and your `template_data`. The
second has only `content_b64`, the base64 of `*** GONFY ***`. The third is
a destination template again: `WithTemplateData` turns rendering on, even
for inline content without a `.tmpl` name.

## Which one to use

> 🦫 **Gonfy says:** Host names and OS differences are carved at the lodge. Anything from your own data can be carved at home.

The recipe sent the same `Site` data down both paths, and the run and the
plan above show where they differ:

- `site.conf` is a destination template. The plan carried the template and
  its data, and the host rendered it while applying. That is why its first
  line names the host (`vm`), its OS and its profile, and why
  `-profile fedora` changed it.
- `banner.txt` is a controller template. `RenderTemplate` rendered it while
  your gonf recorded, so the plan only carried `*** GONFY ***` and the host
  never saw a template.

So for each file, ask whether its content depends on the host it lands on.
If it does, as the first line of `site.conf` does, render it on the
destination; only the host knows its own facts. If the content only comes
from your recipe's data, as the banner does, render it on the controller:
the plan holds exactly the text that will be written, and a render error
shows up on the controller instead of on the host. The table sums up
the two paths:

| | Destination (`.tmpl` source, `WithTemplate`, `WithTemplateData`) | Controller (`RenderTemplate`) |
|--|--|--|
| In this chapter | `site.conf` | `banner.txt` |
| Renders | while applying | while recording |
| Data | your data (also as `.Data`), `.Gonf` facts, environment, `.Param` | your data (also as `.Data`) only |
| Result in the plan | template and data | plain content |
| Use for | host names, OS differences | files built from recipe data, secrets |

Both paths have the helpers `join`, `lower`, `upper`, `trim` and `replace`
(the templates above use `join` and `upper`), and both fail on an unknown
key instead of printing `<no value>`.

`WithContentFrom(RenderTemplate(...))` takes the `(string, error)` pair
directly, so a render error refuses the file instead of writing an empty
one. When your own code builds content and fails, call
`Refuse(kind, id, err)` (for example `Refuse("File", path, err)`): the
record then fails with your error instead of declaring an empty file.

Reference: [Templates](../reference.md#templates),
[File](../reference.md#file), [Facts](../reference.md#facts).

---

← [4. Editing and validating files](04-editing-files.md) · [Contents](README.md) · Next: [6. Commands and change gates](06-commands.md) →
