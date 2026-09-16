package api

import (
	"sync"

	"github.com/snonux/gonf/internal/logger"
)

// taskFleetStack holds the fleet name associated with the currently running
// task body (RegisterMethods WithFleet). Nested Aggregate → child task pushes
// another frame so FleetHosts always sees the innermost task's fleet.
var (
	taskFleetMu    sync.Mutex
	taskFleetStack []string
)

func pushTaskFleet(name string) {
	taskFleetMu.Lock()
	defer taskFleetMu.Unlock()
	taskFleetStack = append(taskFleetStack, name)
}

func popTaskFleet() {
	taskFleetMu.Lock()
	defer taskFleetMu.Unlock()
	if len(taskFleetStack) == 0 {
		return
	}
	taskFleetStack = taskFleetStack[:len(taskFleetStack)-1]
}

func currentTaskFleet() string {
	taskFleetMu.Lock()
	defer taskFleetMu.Unlock()
	if len(taskFleetStack) == 0 {
		return ""
	}
	return taskFleetStack[len(taskFleetStack)-1]
}

// FleetHosts returns List(MustFleet(fleet).HostNames()...) for the fleet
// associated with the current task via RegisterMethods(..., WithFleet(name)).
// Outside a WithFleet task body it fails fast via logger.Fatal.
func FleetHosts() []string {
	name := currentTaskFleet()
	if name == "" {
		logger.Fatal("FleetHosts: no fleet on the current task (RegisterMethods(..., WithFleet(...)))")
	}
	return List(MustFleet(name).HostNames()...)
}

// resetTaskFleet clears the fleet stack (tests).
func resetTaskFleet() {
	taskFleetMu.Lock()
	defer taskFleetMu.Unlock()
	taskFleetStack = nil
}
