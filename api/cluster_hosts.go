package api

import (
	"sync"

	"github.com/snonux/gonf/internal/logger"
)

// taskClusterStack holds the cluster name associated with the currently running
// task body (RegisterMethods WithCluster). Nested Aggregate → child task pushes
// another frame so ClusterHosts always sees the innermost task's fleet.
var (
	taskClusterMu    sync.Mutex
	taskClusterStack []string
)

func pushTaskCluster(name string) {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	taskClusterStack = append(taskClusterStack, name)
}

func popTaskCluster() {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	if len(taskClusterStack) == 0 {
		return
	}
	taskClusterStack = taskClusterStack[:len(taskClusterStack)-1]
}

func currentTaskCluster() string {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	if len(taskClusterStack) == 0 {
		return ""
	}
	return taskClusterStack[len(taskClusterStack)-1]
}

// ClusterHosts returns List(MustCluster(fleet).HostNames()...) for the cluster
// associated with the current task via RegisterMethods(..., WithCluster(name)).
// Outside a WithCluster task body it fails fast via logger.Fatal.
func ClusterHosts() []string {
	name := currentTaskCluster()
	if name == "" {
		logger.Fatal("ClusterHosts: no fleet on the current task (RegisterMethods(..., WithCluster(...)))")
	}
	return List(MustCluster(name).HostNames()...)
}

// resetTaskCluster clears the cluster stack (tests).
func resetTaskCluster() {
	taskClusterMu.Lock()
	defer taskClusterMu.Unlock()
	taskClusterStack = nil
}
