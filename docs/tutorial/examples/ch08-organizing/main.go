// Command gonf organises tasks with structs, aggregates, aliases and
// dependencies (tutorial chapter 8).
package main

import (
	"fmt"
	"strings"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
	"github.com/snonux/gonf/docs/tutorial/examples/ch08-organizing/web"
)

// Backup's methods become backup_* tasks: the prefix of a type in package
// main is just its type name. Its descriptions are hand-written DescX
// companions, the other way to describe a task.
type Backup struct{}

// DescNightly returns the -list description of the Nightly task.
func (Backup) DescNightly() string { return "Nightly backup job" }

// Nightly installs the backup cron job.
func (Backup) Nightly() {
	CronAt("backup", "0 3 * * *", "tar czf /tmp/site.tgz "+Home("gonf-tutorial/site"))
}

// DescRestore returns the -list description of the Restore task.
func (Backup) DescRestore() string { return "Restore the site from the last backup" }

// Restore unpacks the last backup.
func (Backup) Restore() {
	Command("tar", List("xzf", "/tmp/site.tgz", "-C", "/"), WithName("restore"))
}

// OptsRestore marks Restore as an explicit action: no pattern aggregate
// ever picks it up.
func (Backup) OptsRestore() TaskOptions { return TaskOptions{Operational()} }

// sitemap calls Run inside a task body: the web_* tasks' ops join this
// plan. Matching and Tasks read the registered tasks while recording.
func sitemap() {
	_ = Run(Matching("^web_")...) // a failure also fails this record
	var list strings.Builder
	for _, t := range Tasks() {
		if strings.HasPrefix(t.Name, "web_") {
			fmt.Fprintf(&list, "%s: %s\n", t.Name, t.Description)
		}
	}
	File(DestHome("gonf-tutorial/site/htdocs/tasks.txt"), WithContent(list.String()), WithMode(0o644))
}

func main() {
	RegisterMethods(web.Web{}) // web_docroot, web_config, web_content, web_logrotate
	RegisterMethods(Backup{})  // backup_nightly, backup_restore

	AggregatePrefix("web") // the task "web" runs every web_* task
	AggregateTasks("all", "The website, then its backup", "web", "backup_nightly")
	Alias("deploy", "", "web")
	Task("sitemap", "Run every web_* task, then list them in the site", sitemap)
	cli.Main()
}
