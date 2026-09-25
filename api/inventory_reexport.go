package api

import (
	"github.com/snonux/gonf/internal/privilege"
	pubinv "github.com/snonux/gonf/inventory"
)

// Inventory declarations live in package inventory, which carries their
// documentation; api re-exports them so a recipe needs only the api import.

// Inventory handle, option and listing types.
type (
	HostRef     = pubinv.HostRef
	HostOption  = pubinv.HostOption
	ClusterRef  = pubinv.ClusterRef
	HostInfo    = pubinv.HostInfo
	ClusterInfo = pubinv.ClusterInfo
	FleetRef    = pubinv.FleetRef
	FleetInfo   = pubinv.FleetInfo
)

// WithSSHUser is inventory.WithSSHUser.
func WithSSHUser(user string) HostOption { return pubinv.WithSSHUser(user) }

// WithSSHHost is inventory.WithSSHHost.
func WithSSHHost(host string) HostOption { return pubinv.WithSSHHost(host) }

// WithSSHDomain is inventory.WithSSHDomain.
func WithSSHDomain(domain string) HostOption { return pubinv.WithSSHDomain(domain) }

// WithSSHPort is inventory.WithSSHPort.
func WithSSHPort(port int) HostOption { return pubinv.WithSSHPort(port) }

// WithSSHIdentity is inventory.WithSSHIdentity.
func WithSSHIdentity(path string) HostOption { return pubinv.WithSSHIdentity(path) }

// WithPrivilege is inventory.WithPrivilege.
func WithPrivilege(mode privilege.Mode) HostOption { return pubinv.WithPrivilege(mode) }

// WithPlanRecipient is inventory.WithPlanRecipient.
func WithPlanRecipient(recipient string) HostOption { return pubinv.WithPlanRecipient(recipient) }

// WithGOOS is inventory.WithGOOS.
func WithGOOS(goos string) HostOption { return pubinv.WithGOOS(goos) }

// WithGOARCH is inventory.WithGOARCH.
func WithGOARCH(goarch string) HostOption { return pubinv.WithGOARCH(goarch) }

// WithPlatform is inventory.WithPlatform.
func WithPlatform(platform string) HostOption { return pubinv.WithPlatform(platform) }

// WithGonfPath is inventory.WithGonfPath.
func WithGonfPath(path string) HostOption { return pubinv.WithGonfPath(path) }

// WithValue is inventory.WithValue.
func WithValue(key string, value any) HostOption { return pubinv.WithValue(key, value) }

// Host is inventory.Host.
func Host(name string, opts ...HostOption) HostRef { return pubinv.Host(name, opts...) }

// Cluster is inventory.Cluster.
func Cluster(name string, hosts ...HostRef) ClusterRef { return pubinv.Cluster(name, hosts...) }

// LookupHost is inventory.LookupHost.
func LookupHost(name string) (HostRef, bool) { return pubinv.LookupHost(name) }

// LookupCluster is inventory.LookupCluster.
func LookupCluster(name string) (ClusterRef, bool) { return pubinv.LookupCluster(name) }

// MustHost is inventory.MustHost.
func MustHost(name string) HostRef { return pubinv.MustHost(name) }

// MustCluster is inventory.MustCluster.
func MustCluster(name string) ClusterRef { return pubinv.MustCluster(name) }

// Hosts is inventory.Hosts.
func Hosts() []HostInfo { return pubinv.Hosts() }

// Clusters is inventory.Clusters.
func Clusters() []ClusterInfo { return pubinv.Clusters() }

// ResetInventory is inventory.ResetInventory.
func ResetInventory() { pubinv.ResetInventory() }

// HostDefaults is inventory.HostDefaults.
func HostDefaults(opts ...HostOption) HostOption { return pubinv.HostDefaults(opts...) }

// WithData is inventory.WithData.
func WithData(v any) HostOption { return pubinv.WithData(v) }

// Fleet is inventory.Fleet.
func Fleet(name string, clusters ...ClusterRef) FleetRef { return pubinv.Fleet(name, clusters...) }

// LookupFleet is inventory.LookupFleet.
func LookupFleet(name string) (FleetRef, bool) { return pubinv.LookupFleet(name) }

// MustFleet is inventory.MustFleet.
func MustFleet(name string) FleetRef { return pubinv.MustFleet(name) }

// Fleets is inventory.Fleets.
func Fleets() []FleetInfo { return pubinv.Fleets() }

// Privilege modes for WithPrivilege.
const (
	PrivilegeNone = pubinv.PrivilegeNone
	PrivilegeSudo = pubinv.PrivilegeSudo
	PrivilegeDoas = pubinv.PrivilegeDoas
)
