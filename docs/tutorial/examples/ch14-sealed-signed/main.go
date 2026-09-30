// Command gonf seals one plan per host (tutorial chapter 14).
package main

import (
	_ "embed"

	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
)

// Each host's public age key, saved from the host itself. The private
// halves never leave the hosts.
var (
	//go:embed keys/earth.pub
	earthKey string
	//go:embed keys/mars.pub
	marsKey string
)

// Lodge's tasks apply to the "inner" cluster.
type Lodge struct{}

// DescToken returns the -list description of the Token task.
func (Lodge) DescToken() string { return "Install each lodge's own API token" }

// Token installs the host's own token, secrets/lodge/<host> (a dummy
// value in the tutorial). With plan -for, ForHosts runs only for the host
// a file is sealed for.
func (Lodge) Token() {
	dir := DestHome("gonf-tutorial/lodge")
	Dir(dir, WithMode(0o700))
	ForHosts("token", func(host, ref string) {
		File(dir+"/token", WithContent(MustSecret(ref)), WithMode(0o600))
	})
}

func main() {
	earth := Host("earth", WithSSHUser("paul"), WithSSHDomain("lan"),
		WithValue("token", "lodge/earth"), WithPlanRecipient(earthKey))
	mars := Host("mars", WithSSHUser("paul"), WithSSHDomain("lan"),
		WithValue("token", "lodge/mars"), WithPlanRecipient(marsKey))
	Cluster("inner", earth, mars)
	RegisterOnCluster("inner", Lodge{}) // lodge_token
	cli.Main()
}
