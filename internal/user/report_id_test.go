package user

import (
	"testing"

	"github.com/snonux/gonf/resource"
)

// TestBackendsReportUnderTheCallersID pins task 272: every backend reports
// account mutations under the ID its caller passes (the resource layer's
// registered ID), never under one it spells itself, and reports a created
// group under its own Group[name] ID, not the account's.
func TestBackendsReportUnderTheCallersID(t *testing.T) {
	originalDryRun := resource.DryRun()
	t.Cleanup(func() {
		resource.SetDryRun(originalDryRun)
		resource.ResetReport()
	})
	resource.SetDryRun(true)
	for _, goos := range SupportedGOOS() {
		t.Run(goos, func(t *testing.T) {
			resource.ResetReport()
			host := &fakeHost{t: t, missingGroups: map[string]bool{"newgrp": true}}
			backend, _ := ForGOOS(goos, host.run)
			if err := backend.Ensure("Account[custom]", DesiredUser{Name: "svc", PrimaryGroup: "newgrp"}); err != nil {
				t.Fatalf("Ensure() = %v", err)
			}
			if !resource.AnyChanged("Group[newgrp]") {
				t.Fatalf("group creation not reported under Group[newgrp] (probes %q)", host.calls)
			}
			if !resource.AnyChanged("Account[custom]") {
				t.Fatalf("creation not reported under the caller's ID (commands %q)", host.calls)
			}
			if resource.AnyChanged("User[svc]") {
				t.Fatal("backend reported under a self-spelled User[svc] ID")
			}
		})
	}
}

func TestGroupIDUsesCanonicalFormat(t *testing.T) {
	if got, want := groupID("wheel"), resource.FormatID("Group", "wheel"); got != want || got != "Group[wheel]" {
		t.Fatalf("groupID(wheel) = %q, want %q (= Group[wheel])", got, want)
	}
}
