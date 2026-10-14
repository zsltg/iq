package diff

import (
	"encoding/json"
	"fmt"

	"github.com/wI2L/jsondiff"
)

// Patch renders the change from a to b as an RFC 6902 JSON Patch document. It calls
// jsondiff.Compare with default options (no Factorize, Rationalize, or experimental
// LCS) so the op shape is predictable. It returns the marshaled JSON, so the
// third-party patch type does not cross this package's boundary. Identical values yield
// the literal empty array [], not JSON null, so the output is always a valid patch
// document. Compare marshals a and b through encoding/json, so two numbers of the same
// magnitude (an int and a float64) that round-trip to the same JSON produce no op.
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
