// Command gonf runs Gonfy's website on a fleet of Linux front ends
// (tutorial chapter 16).
package main

import (
	//lint:ignore ST1001 recipes use the unqualified gonf DSL.
	. "github.com/snonux/gonf/api"
	"github.com/snonux/gonf/cli"
	"github.com/snonux/gonf/docs/tutorial/examples/ch16-web-fleet/frontend"
)

func main() {
	// What every front end shares: how to reach it and how to become root.
	fe := HostDefaults(WithSSHUser("paul"), WithSSHDomain("lan"),
		WithPrivilege(PrivilegeSudo))
	fe1 := Host("fe1", fe, WithData(frontend.Lodge{RotateAt: "04:00:00"}))
	fe2 := Host("fe2", fe, WithData(frontend.Lodge{RotateAt: "04:10:00"}))
	fe3 := Host("fe3", fe, WithData(frontend.Lodge{RotateAt: "04:20:00"}))
	Cluster("frontends", fe1, fe2, fe3).Parallel(3)

	// frontend_packages, frontend_account, ... bound to the cluster.
	RegisterOnCluster("frontends", frontend.Frontend{}, Privileged())
	// The task "frontend" runs every frontend_* task.
	AggregatePrefix("frontend")
	cli.Main()
}
