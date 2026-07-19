package diff

import (
	"encoding/json"
	"fmt"

	"github.com/wI2L/jsondiff"
)

// Patch renders the change from a to b as an RFC 6902 JSON Patch document. It
// wraps jsondiff.Compare with default options — no Factorize, Rationalize, or the
// experimental LCS, so the op shape is predictable — and keeps the third-party
// patch type from crossing this package's boundary by returning the marshaled
// JSON. Identical values yield the literal empty array [] rather than JSON null,
// so the output is always a valid patch document. Compare marshals a and b through
// encoding/json, so two numbers of the same magnitude (an int and a float64) that
// round-trip to the same JSON produce no op.
func Patch(a, b any) (json.RawMessage, error) {
	patch, err := jsondiff.Compare(a, b)
	if err != nil {
		return nil, fmt.Errorf("compute json patch: %w", err)
	}
	if len(patch) == 0 {
		return json.RawMessage("[]"), nil
	}
	out, err := json.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("marshal json patch: %w", err)
	}
	return out, nil
}
