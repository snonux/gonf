package api

import (
	"reflect"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/snonux/gonf/plan"
)

// This file is task 4b2 (w82 phase 2, docs/design/plan-encryption.md): the api-level
// pieces `gonf plan -seal -for` (internal/cli/plan_seal_for.go) is built on —
// WithPlanRecipient, HostPlanRecipient, PlanRecipientTargetHosts and
// RecordPlanForHost. The CLI-level host-isolation proof (a per-host sealed
// artifact never carries another host's ForHosts secret) lives in
// internal/cli/plan_seal_for_test.go, since it needs the real -seal/-for
// command path end to end; these tests cover the api package's own pieces
// directly, the same way host_selection_test.go covers ForHosts selection.

// genPlanRecipient returns a fresh age1pq hybrid recipient line, generated
// through filippo.io/age directly — the same technique
// internal/cli/plan_seal_test.go's genSealKeyPair uses, since plan/seal
// exposes no key-generation API of its own.
func genPlanRecipient(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatalf("generate hybrid identity: %v", err)
	}
	return id.Recipient().String()
}

// TestWithPlanRecipientStoresCanonicalRecipient: a host's recipient round
// trips through HostPlanRecipient; a host with no WithPlanRecipient, and an
// unregistered name, both report "no recipient" rather than an empty string
// standing in for one.
func TestWithPlanRecipientStoresCanonicalRecipient(t *testing.T) {
	resetForHostsState(t)
	recipient := genPlanRecipient(t)
	Host("h", WithPlanRecipient(recipient))
	Host("bare")

	if got, ok := HostPlanRecipient("h"); !ok || got != recipient {
		t.Fatalf("HostPlanRecipient(h) = %q, %v; want %q, true", got, ok, recipient)
	}
	if _, ok := HostPlanRecipient("bare"); ok {
		t.Fatalf("HostPlanRecipient(bare) reported a recipient; host set none")
	}
	if _, ok := HostPlanRecipient("unregistered"); ok {
		t.Fatalf("HostPlanRecipient(unregistered) reported a recipient for an unknown host")
	}
}

// TestPlanRecipientTargetHosts covers -for's own name resolution: a host by
// itself, a cluster's members, a fleet's deduplicated members, and an
// unknown name refused by name.
func TestPlanRecipientTargetHosts(t *testing.T) {
	resetForHostsState(t)
	h1 := Host("h1", WithPlanRecipient(genPlanRecipient(t)))
	h2 := Host("h2", WithPlanRecipient(genPlanRecipient(t)))
	Cluster("edge", h1, h2)
	Fleet("edge-fleet", Cluster("solo", h1))

	cases := []struct {
		name string
		want []string
	}{
		{"h1", []string{"h1"}},
		{"edge", []string{"h1", "h2"}},
		{"edge-fleet", []string{"h1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanRecipientTargetHosts(tc.name)
			if err != nil {
				t.Fatalf("PlanRecipientTargetHosts(%q) = %v", tc.name, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PlanRecipientTargetHosts(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}

	if _, err := PlanRecipientTargetHosts("nope"); err == nil ||
		!strings.Contains(err.Error(), `"nope" is not a registered host, cluster or fleet`) {
		t.Fatalf("PlanRecipientTargetHosts(nope) = %v, want the not-registered refusal", err)
	}
}

// TestRecordPlanForHostMatchesPushSelection: RecordPlanForHost(host, ...)
// records with exactly the ForHosts selection a push to that host would use
// (inventory.SelectionForHosts) — an alias's primary name stays selected
// alongside it, matching host_selection_test.go's
// "PushHost of an alias keeps the primary name" case for PushHost("h1-wg").
func TestRecordPlanForHostMatchesPushSelection(t *testing.T) {
	setupForHostsInventory(t)
	visited := registerForHostsTask("iter", "all")
	if _, err := RecordPlanForHost("h1-wg", "for-host", plan.NewMemoryStore(), "iter"); err != nil {
		t.Fatalf("RecordPlanForHost: %v", err)
	}
	want := []string{"h1", "h1-wg"}
	if got := visitedHosts(*visited); !reflect.DeepEqual(got, want) {
		t.Fatalf("visited = %v, want %v", got, want)
	}
}

// TestRecordPlanForHostSelectionDoesNotLeak: the selection RecordPlanForHost
// installs for its one call does not outlive it — a following plain
// RecordPlan visits every host again, exactly like recordPlanForHosts'
// existing restore-on-return guarantee (see runSelectionCase).
func TestRecordPlanForHostSelectionDoesNotLeak(t *testing.T) {
	setupForHostsInventory(t)
	visited := registerForHostsTask("iter", "all")
	if _, err := RecordPlanForHost("h2", "for-host", plan.NewMemoryStore(), "iter"); err != nil {
		t.Fatalf("RecordPlanForHost: %v", err)
	}
	*visited = nil
	if _, err := RecordPlan("after", "", "iter"); err != nil {
		t.Fatalf("RecordPlan: %v", err)
	}
	if got := visitedHosts(*visited); !reflect.DeepEqual(got, everyForHostsHost) {
		t.Fatalf("selection leaked past RecordPlanForHost: visited %v", got)
	}
}
