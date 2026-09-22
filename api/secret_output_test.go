package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/snonux/gonf/api/options"
	"github.com/snonux/gonf/internal/logger"
)

// fakePEM is a synthetic multi-line secret shaped like a PEM private key.
const fakePEM = "-----BEGIN FAKE PRIVATE KEY-----\n" +
	"MIIfakeKEYline1AAAAbbbbCCCCddddEEEEffffGGGGhhhhIIIIjjjjKKKKllll\n" +
	"MIIfakeKEYline2mmmmNNNNooooPPPPqqqqRRRRssssTTTTuuuuVVVVwwwwXXXX\n" +
	"-----END FAKE PRIVATE KEY-----\n"

// A multi-line secret relayed line by line (an elevated child or ssh
// printing it) is redacted line by line; its lines alone never mark or
// refuse an op (the whole secret still does).
func TestMultiLineSecretIsRedactedLineByLine(t *testing.T) {
	ops, err := recordWithSecret(t, fakePEM, func() {
		key := MustSecret("svc/key")
		File("/etc/one-line", options.WithContent(strings.Split(key, "\n")[1]+"\n"))
		File("/etc/whole", options.WithContent(key))
	})
	if err != nil {
		t.Fatal(err)
	}
	if opByPath(t, ops, "/etc/one-line").Sensitive || !opByPath(t, ops, "/etc/whole").Sensitive {
		t.Fatal("line forms must not mark; the whole secret must")
	}
	var out strings.Builder
	w := logger.NewRedactingWriter(&out)
	_, _ = w.Write([]byte("validator echoed:\n" + fakePEM))
	_ = w.Close()
	for _, line := range strings.Split(fakePEM, "\n")[1:3] {
		if strings.Contains(out.String(), line) {
			t.Fatalf("relayed output leaks a key line:\n%s", out.String())
		}
	}
	if !strings.Contains(out.String(), "-----BEGIN FAKE PRIVATE KEY-----") {
		t.Fatalf("PEM armour is not secret and must stay visible:\n%s", out.String())
	}
}

// fakeConfigSecret is a synthetic multi-line secret whose lines include
// shared, ordinary configuration (a section header, a path).
const fakeConfigSecret = "[Interface]\nPrivateKey = fakeWGkey0123456789abcdefXYZ=\n" +
	"metadata_dir = /var/lib/garage/meta\n/var/lib/garage/meta\n"

// The review probes: lines of a multi-line secret that are ordinary
// configuration or paths neither mark an unrelated op (a CA bundle sharing
// PEM armour, a config header) nor refuse an identity that equals a line.
func TestMultiLineSecretLinesNeverMarkOrRefuse(t *testing.T) {
	ops, err := recordWithSecret(t, fakeConfigSecret, func() {
		_ = MustSecret("svc/key")
		Dir("/var/lib/garage/meta")
		File("/etc/wg0.conf.head", options.WithContent("[Interface]\n"))
		File("/etc/ssl/ca.pem", options.WithContent("-----BEGIN CERTIFICATE-----\nMIIfakeCA\n-----END CERTIFICATE-----\n"))
	})
	if err != nil {
		t.Fatalf("a line of a multi-line secret refused an identity: %v", err)
	}
	for _, path := range []string{"/var/lib/garage/meta", "/etc/wg0.conf.head", "/etc/ssl/ca.pem"} {
		if opByPath(t, ops, path).Sensitive {
			t.Errorf("%s: marked sensitive by a line form", path)
		}
	}
}

// Every push/preview summary line (single-host and cluster/fleet share
// summaryOutput) passes through the redactor.
func TestPushSummaryOutputIsRedacted(t *testing.T) {
	if _, err := recordWithSecret(t, fakePlanSecret, func() { _ = MustSecret("svc/key") }); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	old := pushOutput
	pushOutput = &buf
	t.Cleanup(func() { pushOutput = old })
	_, _ = fmt.Fprintf(summaryOutput(), "pushed plan-%s (1 ops) to c (1/1 hosts)\n", fakePlanSecret)
	_, _ = fmt.Fprintf(groupRun{}.writer(), "pushed %s\n", fakePlanSecret)
	if strings.Contains(buf.String(), fakePlanSecret) || strings.Count(buf.String(), "[redacted]") != 2 {
		t.Fatalf("summary output = %q", buf.String())
	}
}

// The reviewer's probe with the real registry: a secret straddling the
// 64 KiB forced flush of an unterminated relayed line stays whole.
func TestRelayForcedFlushKeepsRegistrySecretWhole(t *testing.T) {
	if _, err := recordWithSecret(t, "S3cr3tP@ss\n", func() { _ = MustSecret("svc/key") }); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	w := logger.NewRedactingWriter(&out)
	_, _ = w.Write([]byte(strings.Repeat("x", 65534) + "S3cr"))
	_, _ = w.Write([]byte("3tP@ss\n"))
	_ = w.Close()
	if strings.Contains(out.String(), "S3cr") || strings.Contains(out.String(), "3tP@ss") {
		t.Fatalf("forced flush split the secret: ...%q", out.String()[out.Len()-40:])
	}
}
