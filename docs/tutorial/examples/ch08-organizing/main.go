// Command gonf organises tasks with structs, aggregates, aliases and
// dependencies (tutorial chapter 8).
package main

import (
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

func main() {
	RegisterMethods(web.Web{}) // web_docroot, web_config, web_content, web_logrotate
	RegisterMethods(Backup{})  // backup_nightly, backup_restore

	AggregatePrefix("web") // the task "web" runs every web_* task
	AggregateTasks("all", "The website, then its backup", "web", "backup_nightly")
	Alias("deploy", "", "web")
	cli.Main()
}
