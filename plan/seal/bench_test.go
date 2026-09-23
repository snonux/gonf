package seal

import (
	"bytes"
	"testing"
)

// sealedSize seals a single zero-byte plaintext to recipients and returns
// the resulting artifact's length: with an empty payload, the whole output
// is header, nonce and one empty authenticated chunk, so its growth as
// recipients grows is (to a close approximation) purely the per-recipient
// stanza cost docs/plan-encryption.md "Performance" estimates at ~1.46 KiB.
func sealedSize(tb testing.TB, recipients []Recipient) int {
	tb.Helper()
	var buf bytes.Buffer
	wc, err := Seal(&buf, recipients)
	if err != nil {
		tb.Fatalf("Seal: %v", err)
	}
	if err := wc.Close(); err != nil {
		tb.Fatalf("close: %v", err)
	}
	return buf.Len()
}

// BenchmarkAgePQHeaderSizePerRecipient measures the age1pq header's
// marginal cost per additional recipient by sealing the same empty
// plaintext to 1 and to headerSizeSampleRecipients recipients and dividing
// the size difference by the recipient count difference. It reports the
// result as a custom metric (bytes/recipient) via b.ReportMetric rather
// than through the timing loop, since this benchmark is about size, not
// speed; b.N still governs how many times the measurement is repeated so
// `go test -bench` output includes normal iteration accounting.
//
// Record the measured value in the task's ask annotate note (it should
// land close to the ~1.46 KiB/recipient docs/plan-encryption.md
// "Performance" estimates, but this benchmark is the actual source of
// truth, not that estimate).
const headerSizeSampleRecipients = 10

func BenchmarkAgePQHeaderSizePerRecipient(b *testing.B) {
	one := make([]Recipient, 1)
	one[0], _ = genKeyPair(b)
	many := make([]Recipient, headerSizeSampleRecipients)
	for i := range many {
		many[i], _ = genKeyPair(b)
	}

	sizeOne := sealedSize(b, one)
	sizeMany := sealedSize(b, many)
	perRecipient := float64(sizeMany-sizeOne) / float64(headerSizeSampleRecipients-1)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sealedSize(b, many)
	}
	// Reported after ResetTimer: ResetTimer clears any metric already
	// reported (it is documented to reset "elapsed time" but, in the
	// current runtime, also drops a prior ReportMetric call), so this must
	// come after it, not before, to survive into the benchmark's output.
	b.ReportMetric(perRecipient, "bytes/recipient")
}
