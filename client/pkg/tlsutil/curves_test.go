package tlsutil

import (
	"crypto/tls"
	"testing"
)

func TestGetCurveID_known(t *testing.T) {
	cases := map[string]tls.CurveID{
		"P-256":  tls.CurveP256,
		"P-384":  tls.CurveP384,
		"P-521":  tls.CurveP521,
		"X25519": tls.X25519,
	}
	for name, want := range cases {
		got, err := GetCurveID(name)
		if err != nil {
			t.Fatalf("GetCurveID(%q) returned error: %v", name, err)
		}
		if got != want {
			t.Errorf("GetCurveID(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestGetCurveID_unknown(t *testing.T) {
	if _, err := GetCurveID("not-a-curve"); err == nil {
		t.Fatal("expected error for unknown curve, got nil")
	}
}

func TestGetCurveIDs_preservesOrder(t *testing.T) {
	got, err := GetCurveIDs([]string{"P-384", "P-256"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []tls.CurveID{tls.CurveP384, tls.CurveP256}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %v, want %v", i, got[i], want[i])
		}
	}
}
