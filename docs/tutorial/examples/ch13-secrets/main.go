// Command gonf uses secrets (tutorial chapter 13).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

func main() {
	Task("app", "App config with a database password", func() {
		dir := DestHome("gonf-tutorial/secrets")
		Dir(dir, WithMode(0o700))

		// A file that is exactly one secret: secrets/app/api-token.
		SecretFile(dir+"/api-token", "app/api-token")

		// A secret inside other content. The op is marked sensitive
		// automatically, because its content contains the resolved value.
		password := MustSecret("app/db-password")
		File(dir+"/app.conf",
			WithContent("db_user=app\ndb_password="+password+"\n"), WithMode(0o600))

		// An optional secret: only configure SMTP when one exists.
		if smtp, ok := OptionalSecret("app/smtp-password"); ok {
			File(dir+"/smtp.conf", WithContent("password="+smtp+"\n"), WithMode(0o600))
		}
	})
	cli.Main()
}
