// Package canonical provides the single deterministic serialization and digest
// form shared by proposal request digests, proposal-state integrity digests,
// merge-authorization envelopes, and idempotency request hashes.
//
// One canonical representation feeds validation, idempotency, upstream
// serialization, and audit metadata so a digest always describes exactly the
// request or state it protects.
package canonical

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// JSON marshals v in the platform canonical form:
//
//   - struct fields in declaration order
//   - map keys sorted (encoding/json guarantee)
//   - no insignificant whitespace
//   - no HTML escaping (digests must not depend on encoder escaping policy)
//
// Non-finite floats are rejected so digests never cover NaN/Inf input.
func JSON(v any) ([]byte, error) {
	if err := checkFinite(v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("canonical encode: %w", err)
	}
	// Encoder appends a newline; digests strip it.
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}

// Digest returns SHA-256 hex over domain-separated canonical JSON. The domain
// string prevents a digest computed for one purpose (e.g. a proposal request)
// from ever colliding with another purpose (e.g. proposal state).
func Digest(domain string, v any) (string, error) {
	if domain == "" {
		return "", errors.New("canonical digest domain required")
	}
	payload, err := JSON(v)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{'\n'})
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// MustDigest is Digest for inputs already known to be encodable; an encoding
// failure indicates a programming error and panics.
func MustDigest(domain string, v any) string {
	d, err := Digest(domain, v)
	if err != nil {
		panic(err)
	}
	return d
}

func checkFinite(v any) error {
	switch value := v.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("canonical encode: non-finite float %v", value)
		}
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("canonical encode: non-finite float %v", value)
		}
	case map[string]any:
		for key, item := range value {
			if err := checkFinite(item); err != nil {
				return fmt.Errorf("canonical encode: field %q: %w", key, err)
			}
		}
	case []any:
		for i, item := range value {
			if err := checkFinite(item); err != nil {
				return fmt.Errorf("canonical encode: index %d: %w", i, err)
			}
		}
	case []map[string]any:
		for i, item := range value {
			if err := checkFinite(item); err != nil {
				return fmt.Errorf("canonical encode: index %d: %w", i, err)
			}
		}
	}
	return nil
}
