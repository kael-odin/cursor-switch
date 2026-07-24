package relayauth

import "testing"

func TestProofVerifyRoundTrip(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.HeaderValue() == "" {
		t.Fatal("HeaderValue() is empty")
	}
	if !p.Verify(p.HeaderValue()) {
		t.Fatal("Verify() rejected its own header value")
	}
}

func TestProofVerifyRejectsWrongValue(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	cases := []string{"", "wrong", p.HeaderValue() + "x", p.HeaderValue()[:len(p.HeaderValue())-1]}
	for _, c := range cases {
		if p.Verify(c) {
			t.Fatalf("Verify(%q) = true, want false", c)
		}
	}
}

func TestProofSingletonStable(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	b, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if a.HeaderValue() != b.HeaderValue() {
		t.Fatal("New() returned different proof values across calls; expected process singleton")
	}
}

func TestNilProofVerifyFalse(t *testing.T) {
	var p *Proof
	if p.Verify("anything") {
		t.Fatal("nil Proof.Verify returned true")
	}
	if p.HeaderValue() != "" {
		t.Fatal("nil Proof.HeaderValue returned non-empty")
	}
}
