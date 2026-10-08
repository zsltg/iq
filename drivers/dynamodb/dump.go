package dynamodb

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// ParseKeySchema builds a table's key schema from a ?keys= hint, so an offline dump
// reader keys items exactly as a live scan (which reads the schema from DescribeTable)
// does. The hint is a comma-separated list of key attributes in schema order —
// partition key first, then the optional sort key — each "name" or "name:type" where
// type is S, N, or B (default S), e.g. "pk" or "pk:S,sk:N". A dump carries items but
// not the key schema, so this hint supplies what DescribeTable would.
func ParseKeySchema(hint string) ([]KeyAttr, error) {
	entries := strings.Split(hint, ",")
	keys := make([]KeyAttr, 0, len(entries))
	for _, e := range entries {
		name := strings.TrimSpace(e)
		typ := types.ScalarAttributeTypeS
		if name == "" {
			return nil, fmt.Errorf("dynamodb: empty key attribute in ?keys=%q", hint)
		}
		if n, t, ok := strings.Cut(name, ":"); ok {
			name = strings.TrimSpace(n)
			pt, err := parseScalarType(strings.TrimSpace(t))
			if err != nil {
				return nil, err
			}
			typ = pt
		}
		if name == "" {
			return nil, fmt.Errorf("dynamodb: empty key attribute name in ?keys=%q", hint)
		}
		keys = append(keys, KeyAttr{name: name, typ: typ})
	}
	if len(keys) > 2 {
		return nil, fmt.Errorf("dynamodb: ?keys= has %d attributes, want 1 (partition) or 2 (partition,sort)", len(keys))
	}
	return keys, nil
}

// parseScalarType maps a ?keys= type letter to a DynamoDB scalar attribute type. Only
// S, N, and B are valid key types; anything else is an error so a typo fails fast.
func parseScalarType(s string) (types.ScalarAttributeType, error) {
	switch strings.ToUpper(s) {
	case "S", "STRING":
		return types.ScalarAttributeTypeS, nil
	case "N", "NUMBER":
		return types.ScalarAttributeTypeN, nil
	case "B", "BINARY":
		return types.ScalarAttributeTypeB, nil
	default:
		return "", fmt.Errorf("dynamodb: unknown key type %q: want S, N, or B", s)
	}
}

// ParseItem decodes a DynamoDB-JSON item object — the typed wire shape a native S3
// export (`{"pk":{"S":"…"}}` under each line's "Item") and `aws dynamodb scan` output
// (each element of "Items") both use — into the attribute-value map the read path
// consumes. It is the offline inverse of the SDK's own marshaling, so an item feeds
// Normalize and KeyOf identically to a live scan.
func ParseItem(raw json.RawMessage) (map[string]types.AttributeValue, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("dynamodb: decode item: %w", err)
	}
	item := make(map[string]types.AttributeValue, len(obj))
	for name, v := range obj {
		av, err := parseAttributeValue(v)
		if err != nil {
			return nil, fmt.Errorf("dynamodb: attribute %q: %w", name, err)
		}
		item[name] = av
	}
	return item, nil
}

// parseAttributeValue decodes one DynamoDB-JSON attribute value: a single-key object
// whose key is the type tag (S, N, B, BOOL, NULL, M, L, SS, NS, BS) and whose value is
// the tag's payload. It recurses for M and L. A missing, empty, or unknown tag is an
// error so a malformed dump fails fast rather than dropping data.
func parseAttributeValue(raw json.RawMessage) (types.AttributeValue, error) {
	var tagged map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tagged); err != nil {
		return nil, fmt.Errorf("decode attribute value: %w", err)
	}
	if len(tagged) != 1 {
		return nil, fmt.Errorf("attribute value must have exactly one type tag, got %d", len(tagged))
	}
	for tag, payload := range tagged {
		return decodeTagged(tag, payload)
	}
	return nil, fmt.Errorf("attribute value has no type tag")
}

// scalarDecoders maps each scalar type tag to the decoder of its payload. The
// collection tags are not here: decoding them calls back into parseAttributeValue,
// and a table that holds them would be an initialization cycle.
var scalarDecoders = map[string]func(json.RawMessage) (types.AttributeValue, error){
	"S":    decodeS,
	"N":    decodeN,
	"B":    decodeB,
	"BOOL": decodeBOOL,
	"NULL": decodeNULL,
}

// decodeTagged builds the attribute value for a single type tag and its payload.
func decodeTagged(tag string, payload json.RawMessage) (types.AttributeValue, error) {
	switch tag {
	case "M":
		return decodeM(payload)
	case "L":
		return decodeL(payload)
	case "SS":
		return decodeSS(payload)
	case "NS":
		return decodeNS(payload)
	case "BS":
		return decodeBS(payload)
	}
	if decode, ok := scalarDecoders[tag]; ok {
		return decode(payload)
	}
	return nil, fmt.Errorf("unknown attribute type tag %q", tag)
}

// decodeS builds an S attribute from a JSON string payload.
func decodeS(payload json.RawMessage) (types.AttributeValue, error) {
	s, err := decodeString(payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberS{Value: s}, nil
}

// decodeN builds an N attribute from a JSON string payload, keeping the exact text.
func decodeN(payload json.RawMessage) (types.AttributeValue, error) {
	s, err := decodeString(payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberN{Value: s}, nil
}

// decodeB builds a B attribute from a base64 JSON string payload.
func decodeB(payload json.RawMessage) (types.AttributeValue, error) {
	b, err := decodeBinary(payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberB{Value: b}, nil
}

// decodeBOOL builds a BOOL attribute from a JSON boolean payload.
func decodeBOOL(payload json.RawMessage) (types.AttributeValue, error) {
	v, err := decodeBool("BOOL", payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberBOOL{Value: v}, nil
}

// decodeNULL builds a NULL attribute from a JSON boolean payload. It keeps the
// decoded boolean.
func decodeNULL(payload json.RawMessage) (types.AttributeValue, error) {
	v, err := decodeBool("NULL", payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberNULL{Value: v}, nil
}

// decodeM builds an M attribute from a JSON object of typed attribute values.
func decodeM(payload json.RawMessage) (types.AttributeValue, error) {
	m, err := ParseItem(payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberM{Value: m}, nil
}

// decodeL builds an L attribute from a JSON array of typed attribute values.
func decodeL(payload json.RawMessage) (types.AttributeValue, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(payload, &elems); err != nil {
		return nil, fmt.Errorf("decode L: %w", err)
	}
	list := make([]types.AttributeValue, len(elems))
	for i, e := range elems {
		av, err := parseAttributeValue(e)
		if err != nil {
			return nil, err
		}
		list[i] = av
	}
	return &types.AttributeValueMemberL{Value: list}, nil
}

// decodeSS builds an SS attribute from a JSON array of strings.
func decodeSS(payload json.RawMessage) (types.AttributeValue, error) {
	v, err := decodeStrings("SS", payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberSS{Value: v}, nil
}

// decodeNS builds an NS attribute from a JSON array of number strings.
func decodeNS(payload json.RawMessage) (types.AttributeValue, error) {
	v, err := decodeStrings("NS", payload)
	if err != nil {
		return nil, err
	}
	return &types.AttributeValueMemberNS{Value: v}, nil
}

// decodeBS builds a BS attribute from a JSON array of base64 strings.
func decodeBS(payload json.RawMessage) (types.AttributeValue, error) {
	var encoded []string
	if err := json.Unmarshal(payload, &encoded); err != nil {
		return nil, fmt.Errorf("decode BS: %w", err)
	}
	blobs := make([][]byte, len(encoded))
	for i, e := range encoded {
		b, err := base64.StdEncoding.DecodeString(e)
		if err != nil {
			return nil, fmt.Errorf("decode BS element: %w", err)
		}
		blobs[i] = b
	}
	return &types.AttributeValueMemberBS{Value: blobs}, nil
}

// decodeBool reads a JSON boolean payload. tag names the attribute type in the error.
func decodeBool(tag string, payload json.RawMessage) (bool, error) {
	var v bool
	if err := json.Unmarshal(payload, &v); err != nil {
		return false, fmt.Errorf("decode %s: %w", tag, err)
	}
	return v, nil
}

// decodeStrings reads a JSON array of strings. tag names the attribute type in the
// error.
func decodeStrings(tag string, payload json.RawMessage) ([]string, error) {
	var v []string
	if err := json.Unmarshal(payload, &v); err != nil {
		return nil, fmt.Errorf("decode %s: %w", tag, err)
	}
	return v, nil
}

// decodeString reads a JSON string payload for an S or N attribute.
func decodeString(payload json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(payload, &s); err != nil {
		return "", fmt.Errorf("decode string value: %w", err)
	}
	return s, nil
}

// decodeBinary reads a base64 JSON string payload into raw bytes for a B attribute.
func decodeBinary(payload json.RawMessage) ([]byte, error) {
	s, err := decodeString(payload)
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode B: %w", err)
	}
	return b, nil
}
