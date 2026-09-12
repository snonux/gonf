package plan

import (
	"encoding/base64"
	"fmt"
)

// MaxInlineContent is the largest file packaged as content_b64 during plan
// record. Larger InstallFile sources become blob sidecars under blobs/.
const MaxInlineContent = 512 << 10 // 512 KiB

// DecodeContentB64 decodes a plan file's content_b64 field.
func DecodeContentB64(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("plan: content_b64: empty")
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("plan: content_b64: corrupt base64: %w", err)
	}
	return data, nil
}
