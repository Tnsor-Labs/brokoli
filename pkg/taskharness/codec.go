package taskharness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// encodeFrame renders v as one wire frame: a single-line JSON object
// terminated by LF, per the framing doc. v must marshal to a JSON
// object (every frame type in this package does).
func encodeFrame(v interface{}) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || b[0] != '{' {
		return nil, fmt.Errorf("frame did not marshal to a JSON object")
	}
	if bytes.ContainsRune(b, '\n') {
		return nil, fmt.Errorf("frame contains a literal newline")
	}
	b = append(b, '\n')
	if len(b) > MaxFrameLine {
		return nil, fmt.Errorf("frame is %d bytes, exceeds the %d-byte wire limit", len(b), MaxFrameLine)
	}
	return b, nil
}

// decodeFrameLine decodes one wire line into a field map, enforcing the
// framing rules a per-frame JSON Schema cannot express: valid UTF-8,
// exactly one JSON object, and no duplicate keys. line must not include
// the trailing LF.
func decodeFrameLine(line []byte) (map[string]json.RawMessage, error) {
	if len(line) == 0 {
		return nil, fmt.Errorf("empty line")
	}
	if !utf8.Valid(line) {
		return nil, fmt.Errorf("frame is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("frame is not valid JSON: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("frame is not a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("frame key: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("frame key is not a string")
		}
		if _, dup := fields[key]; dup {
			return nil, fmt.Errorf("frame has duplicate key %q", key)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("frame value for %q: %w", key, err)
		}
		fields[key] = raw
	}
	if _, err := dec.Token(); err != nil { // consume the closing '}'
		return nil, fmt.Errorf("frame: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("frame has trailing content after its JSON object")
	}
	return fields, nil
}

func frameType(fields map[string]json.RawMessage) (string, error) {
	raw, ok := fields["type"]
	if !ok {
		return "", fmt.Errorf("frame has no \"type\" field")
	}
	var t string
	if err := json.Unmarshal(raw, &t); err != nil {
		return "", fmt.Errorf("frame \"type\" is not a string: %w", err)
	}
	return t, nil
}

// decodeInto re-marshals the already duplicate-checked field map and
// unmarshals it into v -- a small round trip, but the harness protocol
// is not a hot data path, and it lets Run reuse ordinary Go struct tags
// for every frame's non-"type" fields instead of hand-extracting each
// one from the map.
func decodeInto(fields map[string]json.RawMessage, v interface{}) error {
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
