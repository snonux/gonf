package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/remote"
	"github.com/snonux/gonf/plan"
)

// forHostsKey is the per-host inventory value every ForHosts test reads.
const forHostsKey = "test.window"

// setupForHostsInventory registers four hosts carrying a typed [2]string
// value: h1 (rex@h1.example), its alias h1-wg (h1.example, no SSH user),
// h2 (rex@h2.example) and h3 (h3.example). Clusters: "all" (every host, in
// that order) and "edge" (h1-wg, h2: a cluster with an alias member); fleet
// "edge-fleet" (edge).
func setupForHostsInventory(t *testing.T) {
	t.Helper()
	resetForHostsState(t)
	h1 := Host("h1", WithSSHUser("rex"), WithSSHHost("h1.example"),
		WithValue(forHostsKey, [2]string{"1", "2"}))
	h1wg := Host("h1-wg", WithSSHHost("h1.example"),
		WithValue(forHostsKey, [2]string{"1", "w"}))
	h2 := Host("h2", WithSSHUser("rex"), WithSSHHost("h2.example"),
		WithValue(forHostsKey, [2]string{"3", "4"}))
	h3 := Host("h3", WithSSHHost("h3.example"),
		WithValue(forHostsKey, [2]string{"5", "6"}))
	Cluster("all", h1, h1wg, h2, h3)
	Fleet("edge-fleet", Cluster("edge", h1wg, h2))
}

// resetForHostsState clears tasks, recording state and inventory now and
// after the test.
func resetForHostsState(t *testing.T) {
	t.Helper()
	ResetForTest()
	ResetInventory()
	t.Cleanup(func() {
		ResetForTest()
		ResetInventory()
	})
}

// registerForHostsTask registers task name on cluster, writing one file per
// visited host, and returns the (host=value) pairs its body visited.
func registerForHostsTask(name, cluster string) *[]string {
	visited := &[]string{}
	Task(name, "", func() {
		ForHosts(forHostsKey, func(host string, w [2]string) {
			*visited = append(*visited, host+"="+w[0]+w[1])
			File("/tmp/for-hosts-"+host, options.WithContent(w[0]+" "+w[1]+"\n"))
		})
	}, WithTaskCluster(cluster))
	return visited
}

// visitedHosts strips the values from registerForHostsTask's pairs.
func visitedHosts(visited []string) []string {
	var hosts []string
	for _, v := range visited {
		hosts = append(hosts, strings.SplitN(v, "=", 2)[0])
	}
	return hosts
}

// whenHosts returns the hostname_contains guards of the recorded ops.
func whenHosts(ops []plan.Op) []string {
	var out []string
	for _, op := range ops {
		if op.Op != plan.KindWhenBegin {
			continue
		}
		for _, p := range op.All {
			if p.Fact == "hostname_contains" {
				out = append(out, p.Eq)
			}
		}
	}
	return out
}

// TestForHostsRecordsSameOpsAsManualLoop pins plan compatibility: without a
// host selection (gonf plan, RecordPlan) ForHosts records exactly the ops of
// the ClusterHosts → MustHostValue → WhenHostname loop it replaces, with one
// destination-side hostname guard per host in registration order.
func TestForHostsRecordsSameOpsAsManualLoop(t *testing.T) {
	setupForHostsInventory(t)
	visited := registerForHostsTask("iter", "all")
	Task("loop", "", func() {
		for _, host := range ClusterHosts() {
			w := MustHostValue[[2]string](host, forHostsKey)
			WhenHostname(host, func() {
				File("/tmp/for-hosts-"+host, options.WithContent(w[0]+" "+w[1]+"\n"))
			})
		}
	}, WithTaskCluster("all"))

	iterOps, err := RecordPlan("compat", "", "iter")
	if err != nil {
		t.Fatalf("RecordPlan(iter): %v", err)
	}
	loopOps, err := RecordPlan("compat", "", "loop")
	if err != nil {
		t.Fatalf("RecordPlan(loop): %v", err)
	}
	if !reflect.DeepEqual(iterOps, loopOps) {
		t.Fatalf("ForHosts ops differ from the manual loop:\n got %#v\nwant %#v", iterOps, loopOps)
	}
	if got, want := whenHosts(iterOps), []string{"h1", "h1-wg", "h2", "h3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("destination guards = %v, want %v", got, want)
	}
	if got, want := *visited, []string{"h1=12", "h1-wg=1w", "h2=34", "h3=56"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("visited = %v, want %v", got, want)
	}
}

// TestForHostsDirectCallRunsOnlyMatchingHostname covers the non-recording
// path: fn runs only for the inventory host whose name the local hostname
// contains, the same local semantics as WhenHostname.
func TestForHostsDirectCallRunsOnlyMatchingHostname(t *testing.T) {
	resetForHostsState(t)
	local := localHostnameForTest(t)
	Cluster("local",
		Host(local, WithValue(forHostsKey, 1)),
		Host("zz-not-this-host", WithValue(forHostsKey, 2)))

	pushTaskCluster("local")
	defer popTaskCluster()
	var visited []string
	ForHosts(forHostsKey, func(host string, v int) {
		visited = append(visited, fmt.Sprintf("%s=%d", host, v))
	})
	if want := []string{local + "=1"}; !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
}

// TestForHostsLocalRunResolvesOnlyLocalHost runs a real local Run: only the
// host whose name the local hostname contains is visited, so its secret is
// the only one needed, and only its fragment is applied.
func TestForHostsLocalRunResolvesOnlyLocalHost(t *testing.T) {
	resetForHostsState(t)
	dir := useSecretWorkDir(t)
	local := localHostnameForTest(t)
	const other = "zz-not-this-host"
	Cluster("local",
		Host(local, WithValue(forHostsKey, "mine")),
		Host(other, WithValue(forHostsKey, "theirs")))
	writeSecret(t, "tokens/"+local, "local-token")

	var visited []string
	Task("local_tokens", "", func() {
		ForHosts(forHostsKey, func(host string, v string) {
			visited = append(visited, host)
			File(filepath.Join(dir, "out-"+v), options.WithContent(MustSecret("tokens/"+host)))
		})
	}, WithTaskCluster("local"))

	if err := Run("local_tokens"); err != nil {
		t.Fatalf("local Run needs only the local host's secret: %v", err)
	}
	if want := []string{local}; !reflect.DeepEqual(visited, want) {
		t.Fatalf("visited = %v, want %v", visited, want)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "out-mine")); err != nil || string(got) != "local-token" {
		t.Fatalf("local fragment not applied: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out-theirs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("other host's fragment applied locally: %v", err)
	}
}

// localHostnameForTest returns the local hostname, skipping when unknown.
func localHostnameForTest(t *testing.T) string {
	t.Helper()
	local, err := os.Hostname()
	if err != nil || local == "" {
		t.Skipf("no local hostname: %v", err)
	}
	return local
}

// TestForHostsRecordErrors checks that ForHosts misuse and inventory errors
// during recording fail the record with an error (not a process exit), visit
// no host, and record no fragment.
func TestForHostsRecordErrors(t *testing.T) {
	cases := []struct {
		name string
		body func(visit func(string, [2]string))
		want string
	}{
		{"missing value on a later host", func(visit func(string, [2]string)) {
			ForHosts(forHostsKey, visit)
		}, `ForHosts: Host "h3": no value "test.window"`},
		{"wrong type", func(func(string, [2]string)) {
			ForHosts(forHostsKey, func(string, string) { panic("visited") })
		}, `ForHosts: Host "h1" value "test.window": want string, got [2]string`},
		{"empty key", func(visit func(string, [2]string)) { ForHosts("", visit) },
			"ForHosts: key must not be empty"},
		{"nil fn", func(func(string, [2]string)) { ForHosts[[2]string](forHostsKey, nil) },
			"ForHosts: fn must not be nil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetForHostsState(t)
			Cluster("c", Host("h1", WithValue(forHostsKey, [2]string{"1", "2"})), Host("h3"))
			var visited []string
			visit := func(host string, _ [2]string) { visited = append(visited, host) }
			Task("bad", "", func() { tc.body(visit) }, WithTaskCluster("c"))
			_, err := RecordPlan("bad", "", "bad")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RecordPlan error = %v, want %q", err, tc.want)
			}
			if len(visited) != 0 {
				t.Fatalf("visited %v before failing", visited)
			}
		})
	}
}

// TestForHostsOutsideWithClusterIsRecordError covers the missing WithCluster
// case, which fails the record like the value errors.
func TestForHostsOutsideWithClusterIsRecordError(t *testing.T) {
	resetForHostsState(t)
	Host("h1", WithValue(forHostsKey, 1))
	Task("nocluster", "", func() { ForHosts(forHostsKey, func(string, int) {}) })
	_, err := RecordPlan("nocluster", "", "nocluster")
	if err == nil || !strings.Contains(err.Error(), "ForHosts: no cluster on the current task") {
		t.Fatalf("RecordPlan error = %v", err)
	}
}

// TestForHostsBadValueFailsLocalRunCleanly is the local-run half of the error
// contract: Run returns the error (no process exit), applies nothing and
// leaves no temporary plan directory behind.
func TestForHostsBadValueFailsLocalRunCleanly(t *testing.T) {
	resetForHostsState(t)
	dir := useSecretWorkDir(t)
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	Cluster("c", Host("h1", WithValue(forHostsKey, 42)))
	target := filepath.Join(dir, "never")
	Task("typo", "", func() {
		File(target, options.WithContent("x"))
		ForHosts(forHostsKey, func(string, string) {})
	}, WithTaskCluster("c"))

	err := Run("typo")
	if err == nil || !strings.Contains(err.Error(), `want string, got int`) {
		t.Fatalf("Run error = %v, want type mismatch", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Run applied a plan after a record error: %v", err)
	}
	left, err := filepath.Glob(filepath.Join(tmp, "gonf-plan-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("Run leaked plan directories: %v", left)
	}
}

// TestForHostsSingleHostPushSkipsOtherHostsSecrets is the selected-host input
// contract: a host-specific MustSecret inside fn is only resolved for the
// hosts being pushed to, so a one-host deploy succeeds without unrelated
// hosts' secrets, while a push reaching a host whose secret is missing still
// fails before any SSH connection.
func TestForHostsSingleHostPushSkipsOtherHostsSecrets(t *testing.T) {
	setupForHostsInventory(t)
	useSecretWorkDir(t)
	writeSecret(t, "tokens/h2", "h2-token\n")
	Task("tokens", "", func() {
		ForHosts(forHostsKey, func(host string, _ [2]string) {
			File("/tmp/token", options.WithContent(MustSecret("tokens/"+host)))
		})
	}, WithTaskCluster("edge"))

	calls := captureSSHSafe(t)
	if err := PushHost(MustHost("h2"), "tokens"); err != nil {
		t.Fatalf("single-host push needs only its own secret: %v", err)
	}
	streamed := calls.all()
	if len(streamed) == 0 || !bytes.Contains(streamed[len(streamed)-1], []byte("GONF-PUSH/1")) {
		t.Fatalf("single-host push did not stream a plan (%d ssh calls)", len(streamed))
	}

	calls.reset()
	err := PushCluster("edge", "tokens")
	if err == nil || !strings.Contains(err.Error(), `secret "tokens/h1-wg" is missing`) {
		t.Fatalf("cluster push error = %v, want missing h1-wg secret", err)
	}
	if n := len(calls.all()); n != 0 {
		t.Fatalf("cluster push opened %d SSH connections after a record failure", n)
	}
}

// TestForHostsUnselectedBadValueFailsPushBeforeSSH: inventory errors are
// checked for every member, so a push to one host still fails (with an
// error, before SSH) when another member's value is missing.
func TestForHostsUnselectedBadValueFailsPushBeforeSSH(t *testing.T) {
	resetForHostsState(t)
	Cluster("c", Host("h1", WithSSHHost("h1.example"), WithValue(forHostsKey, 1)), Host("h3"))
	var visited []string
	Task("iter", "", func() {
		ForHosts(forHostsKey, func(host string, _ int) { visited = append(visited, host) })
	}, WithTaskCluster("c"))
	calls := captureSSHSafe(t)
	err := PushHost(MustHost("h1"), "iter")
	if err == nil || !strings.Contains(err.Error(), `Host "h3": no value`) {
		t.Fatalf("PushHost error = %v, want h3's missing value", err)
	}
	if len(visited) != 0 || len(calls.all()) != 0 {
		t.Fatalf("visited %v / %d ssh calls before failing", visited, len(calls.all()))
	}
	if !hostSelected("h3") {
		t.Fatal("host selection leaked past a failed recording")
	}
}

// TestForHostsMisuseOutsideRecordingIsFatal runs direct (non-recording)
// ForHosts misuse in a helper process: without a recording session to fail,
// it ends the process via logger.Fatal, like MustHostValue.
func TestForHostsMisuseOutsideRecordingIsFatal(t *testing.T) {
	cases := map[string]string{
		"missing-value": `ForHosts: Host "h2": no value "test.window"`,
		"nil-fn":        "ForHosts: fn must not be nil",
		"no-cluster":    "ForHosts: no cluster on the current task",
	}
	for caseName, want := range cases {
		t.Run(caseName, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestForHostsFatalHelperProcess$", "-test.timeout=60s")
			cmd.Env = append(os.Environ(), "GONF_API_FORHOSTS_CASE="+caseName)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("case %q exited 0, want fail-fast; output:\n%s", caseName, out)
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("case %q output misses %q:\n%s", caseName, want, out)
			}
			for _, marker := range []string{"FORHOSTS-VISITED", "FORHOSTS-RETURNED"} {
				if strings.Contains(string(out), marker) {
					t.Fatalf("case %q reached %s before failing:\n%s", caseName, marker, out)
				}
			}
		})
	}
}

// TestForHostsFatalHelperProcess is the helper process for
// TestForHostsMisuseOutsideRecordingIsFatal; it must never exit 0 for a
// known case.
func TestForHostsFatalHelperProcess(t *testing.T) {
	caseName := os.Getenv("GONF_API_FORHOSTS_CASE")
	if caseName == "" {
		return
	}
	ResetInventory()
	visit := func(host string, _ [2]string) { fmt.Println("FORHOSTS-VISITED", host) }
	ok := WithValue(forHostsKey, [2]string{"1", "2"})
	switch caseName {
	case "missing-value":
		Cluster("c", Host("h1", ok), Host("h2"))
		pushTaskCluster("c")
		ForHosts(forHostsKey, visit)
	case "nil-fn":
		Cluster("c", Host("h1", ok))
		pushTaskCluster("c")
		ForHosts[[2]string](forHostsKey, nil)
	case "no-cluster":
		Host("h1", ok)
		ForHosts(forHostsKey, visit)
	}
	fmt.Println("FORHOSTS-RETURNED")
}

// sshCapture records the stdin of every faked SSH invocation. Cluster and
// fleet pushes fan out concurrently, so access is mutex-guarded.
type sshCapture struct {
	mu     sync.Mutex
	stdins [][]byte
}

func (c *sshCapture) all() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.stdins...)
}

func (c *sshCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stdins = nil
}

// captureSSHSafe fakes the SSH transport and every remote gonf probe
// (plan schema, strict preview, release) so no test here reaches a real ssh.
func captureSSHSafe(t *testing.T) *sshCapture {
	t.Helper()
	old := remote.SSHRunner
	restoreProbes := remote.AssumeRemoteGonfCurrent()
	t.Cleanup(func() {
		remote.SSHRunner = old
		restoreProbes()
	})
	c := &sshCapture{}
	remote.SSHRunner = func(_ context.Context, stdin io.Reader, _ []string) error {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, stdin)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.stdins = append(c.stdins, buf.Bytes())
		return nil
	}
	return c
}
