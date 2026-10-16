package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// setJSONMember sets one member of a JSON object document, changing only
// that member's bytes. Everything else (other members, their order,
// formatting and number spellings) is kept exactly. A missing member is
// added after the last one.
//
// Game config files are rewritten by the game itself and can hold values a
// decode and re-encode would change: Go reads every number as a float64, so
// a Java long such as 9223372036854775807 would come back as a different,
// out-of-range number.
func setJSONMember(doc []byte, key string, value any) ([]byte, error) {
	encoded, err := encodeJSONValue(value)
	if err != nil {
		return nil, err
	}
	return setJSONMemberRaw(doc, key, encoded)
}

func setJSONMemberRaw(doc []byte, key string, encoded []byte) ([]byte, error) {
	start, end, lastEnd, closeAt, err := findJSONMember(doc, key)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	switch {
	case start >= 0:
		out.Write(doc[:start])
		out.Write(encoded)
		out.Write(doc[end:])
	case lastEnd >= 0:
		name, _ := encodeJSONValue(key)
		out.Write(doc[:lastEnd])
		out.WriteString(",\n  ")
		out.Write(name)
		out.WriteString(": ")
		out.Write(encoded)
		out.Write(doc[lastEnd:])
	default:
		name, _ := encodeJSONValue(key)
		out.Write(doc[:closeAt])
		out.WriteString("\n  ")
		out.Write(name)
		out.WriteString(": ")
		out.Write(encoded)
		out.WriteString("\n")
		out.Write(doc[closeAt:])
	}
	return out.Bytes(), nil
}

// jsonMember returns the raw bytes of a member, or nil when it is missing.
func jsonMember(doc []byte, key string) ([]byte, error) {
	start, end, _, _, err := findJSONMember(doc, key)
	if err != nil || start < 0 {
		return nil, err
	}
	return doc[start:end], nil
}

// findJSONMember locates key's value in a JSON object. It returns the value's
// byte range (-1 when missing), the end of the last member's value (-1 when
// the object is empty) and the offset of the closing brace.
func findJSONMember(doc []byte, key string) (start, end, lastEnd, closeAt int, err error) {
	start, end, lastEnd = -1, -1, -1
	dec := json.NewDecoder(bytes.NewReader(doc))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, 0, 0, 0, errors.New("not a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return 0, 0, 0, 0, err
		}
		name, ok := tok.(string)
		if !ok {
			return 0, 0, 0, 0, errors.New("not a JSON object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, 0, 0, 0, err
		}
		valueEnd := int(dec.InputOffset())
		if name == key && start < 0 {
			start, end = valueEnd-len(raw), valueEnd
		}
		lastEnd = valueEnd
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return 0, 0, 0, 0, errors.New("not a JSON object")
	}
	closeAt = int(dec.InputOffset()) - 1
	if _, err := dec.Token(); err == nil {
		return 0, 0, 0, 0, errors.New("unexpected data after the JSON object")
	}
	return start, end, lastEnd, closeAt, nil
}

// encodeJSONValue encodes without HTML escaping, so text such as "<gold>"
// in a message stays readable in the file.
func encodeJSONValue(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encode %v: %w", v, err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
