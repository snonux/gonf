package plan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// EncodeOp marshals a single plan line to JSON without a trailing newline.
func EncodeOp(op Op) ([]byte, error) {
	if op.Op == "" {
		return nil, fmt.Errorf("plan: encode: missing op")
	}
	normalizeOp(&op)
	b, err := json.Marshal(op)
	if err != nil {
		return nil, fmt.Errorf("plan: encode %s: %w", op.Op, err)
	}
	return b, nil
}

// DecodeOp unmarshals one JSONL plan line into an Op.
func DecodeOp(line []byte) (Op, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Op{}, fmt.Errorf("plan: decode: empty line")
	}
	var op Op
	if err := json.Unmarshal(line, &op); err != nil {
		return Op{}, fmt.Errorf("plan: decode: %w", err)
	}
	if op.Op == "" {
		return Op{}, fmt.Errorf("plan: decode: missing op")
	}
	normalizeOp(&op)
	return op, nil
}

func normalizeOp(op *Op) {
	if len(op.Args) == 0 {
		op.Args = nil
	}
	if len(op.Env) == 0 {
		op.Env = nil
	}
	if len(op.All) == 0 {
		op.All = nil
	}
	if len(op.CronEnv) == 0 {
		op.CronEnv = nil
	}
	if len(op.Deps) == 0 {
		op.Deps = nil
	}
	if op.Unless != nil {
		normalizeGuard(op.Unless)
	}
	if op.OnlyIf != nil {
		normalizeGuard(op.OnlyIf)
	}
}

func normalizeGuard(g *Guard) {
	if len(g.Args) == 0 {
		g.Args = nil
	}
}

// EncodePlan writes ops as JSONL (each line one Op, including trailing newline
// after the last line when ops is non-empty).
func EncodePlan(ops []Op) ([]byte, error) {
	if len(ops) == 0 {
		return nil, fmt.Errorf("plan: encode: empty plan")
	}
	var buf bytes.Buffer
	for i, op := range ops {
		b, err := EncodeOp(op)
		if err != nil {
			return nil, fmt.Errorf("plan: encode line %d: %w", i+1, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// DecodePlan parses a full JSONL document and enforces the version gate on the
// header before returning. It refuses missing/non-plan headers and unsupported
// versions without interpreting later lines as apply work.
func DecodePlan(r io.Reader) ([]Op, error) {
	br := bufio.NewReader(r)
	line, err := readNonEmptyLine(br)
	if err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("plan: missing header: empty input")
		}
		return nil, fmt.Errorf("plan: read header: %w", err)
	}

	header, err := DecodeOp(line)
	if err != nil {
		return nil, fmt.Errorf("plan: header: %w", err)
	}
	if err := ValidateHeader(header); err != nil {
		return nil, err
	}

	ops := []Op{header}
	for lineNo := 2; ; lineNo++ {
		line, err := readNonEmptyLine(br)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("plan: read line %d: %w", lineNo, err)
		}
		op, err := DecodeOp(line)
		if err != nil {
			return nil, fmt.Errorf("plan: line %d: %w", lineNo, err)
		}
		if op.Op == KindPlan {
			return nil, fmt.Errorf("plan: line %d: duplicate plan header", lineNo)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// DecodePlanBytes is DecodePlan over a byte slice.
func DecodePlanBytes(data []byte) ([]Op, error) {
	return DecodePlan(bytes.NewReader(data))
}

// ValidateHeader checks the first plan line: KindPlan, required version, and
// SupportsVersion. Callers must invoke this before any filesystem mutation.
func ValidateHeader(header Op) error {
	if header.Op != KindPlan {
		return fmt.Errorf("plan: first op must be %q, got %q", KindPlan, header.Op)
	}
	if header.Version == 0 {
		return fmt.Errorf("plan: header missing version")
	}
	if !SupportsVersion(header.Version) {
		return fmt.Errorf("unsupported plan version %d (this gonf supports %s)", header.Version, FormatSupportedVersions())
	}
	return nil
}

func readNonEmptyLine(br *bufio.Reader) ([]byte, error) {
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			// Trim trailing newline(s) but keep content for DecodeOp TrimSpace.
			line = bytes.TrimRight(line, "\r\n")
			if len(bytes.TrimSpace(line)) > 0 {
				if err == io.EOF {
					return line, nil
				}
				return line, err
			}
		}
		if err != nil {
			return nil, err
		}
	}
}

// FormatSupportedVersions returns a human-readable list of supported versions.
func FormatSupportedVersions() string {
	versions := make([]int, 0, len(supportedVersions))
	for v := range supportedVersions {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	ids := make([]string, 0, len(versions))
	for _, v := range versions {
		ids = append(ids, fmt.Sprintf("%d", v))
	}
	return strings.Join(ids, ", ")
}
