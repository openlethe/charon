package canonical

import (
	"math"
	"strings"
	"testing"
)

func TestJSONDeterministicMapOrder(t *testing.T) {
	a, err := JSON(map[string]any{"b": 1, "a": 2, "c": map[string]any{"z": 1, "y": 2}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := JSON(map[string]any{"c": map[string]any{"y": 2, "z": 1}, "b": 1, "a": 2})
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical JSON not key-order independent:\n%s\n%s", a, b)
	}
	if strings.Contains(string(a), "<") {
		t.Fatalf("unexpected escaping: %s", a)
	}
}

func TestJSONRejectsNonFinite(t *testing.T) {
	cases := []any{
		math.NaN(),
		map[string]any{"x": math.Inf(1)},
		[]any{1.0, math.NaN()},
	}
	for i, c := range cases {
		if _, err := JSON(c); err == nil {
			t.Fatalf("case %d: expected non-finite rejection", i)
		}
	}
}

func TestDigestDomainSeparation(t *testing.T) {
	v := struct {
		A string `json:"a"`
	}{A: "x"}
	d1, err := Digest("purpose/one", v)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := Digest("purpose/two", v)
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Fatal("domain separation failed")
	}
	if len(d1) != 64 {
		t.Fatalf("digest length %d", len(d1))
	}
	if _, err := Digest("", v); err == nil {
		t.Fatal("empty domain must fail")
	}
}

func TestDigestStableAcrossCalls(t *testing.T) {
	v := map[string]any{"k": []any{3.0, 2.0, 1.0}}
	a := MustDigest("test/v1", v)
	b := MustDigest("test/v1", v)
	if a != b {
		t.Fatal("digest unstable")
	}
}
