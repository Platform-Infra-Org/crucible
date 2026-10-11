package secrets

import (
	"strings"
	"testing"
)

const key = "a-long-enough-development-key"

func box(t *testing.T, pass string) *Box {
	t.Helper()
	b, err := New(pass)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealOpenRoundTrip(t *testing.T) {
	b := box(t, key)
	for _, plain := range []string{"ghp_token", "", strings.Repeat("x", 4096), "ünïcode ✓", "line\nbreak"} {
		sealed, err := b.Seal(plain)
		if err != nil {
			t.Fatalf("seal %q: %v", plain, err)
		}
		got, err := b.Open(sealed)
		if err != nil || got != plain {
			t.Fatalf("round trip %q: got %q, %v", plain, got, err)
		}
	}
}

// The stored form must not contain the credential: that is the whole point of sealing it before it reaches Postgres.
func TestSealedValueDoesNotContainThePlaintext(t *testing.T) {
	sealed, err := box(t, key).Seal("ghp_SuperSecretToken")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "SuperSecret") {
		t.Fatalf("plaintext survives in %q", sealed)
	}
	if !strings.HasPrefix(sealed, prefix) {
		t.Fatalf("stored value must carry the scheme prefix: %q", sealed)
	}
}

// Two seals of one value differ, so the database never shows that two repositories share a credential.
func TestSealIsNotDeterministic(t *testing.T) {
	b := box(t, key)
	a, _ := b.Seal("same")
	c, _ := b.Seal("same")
	if a == c {
		t.Fatal("two seals of the same value are identical: the nonce is not fresh")
	}
}

// A rotated or mistyped key must fail loudly, never return something that looks like a credential.
func TestAnotherKeyCannotOpenIt(t *testing.T) {
	sealed, _ := box(t, key).Seal("ghp_token")
	got, err := box(t, "a-different-but-long-key").Open(sealed)
	if err == nil {
		t.Fatalf("opened with the wrong key: %q", got)
	}
	if !strings.Contains(err.Error(), "CRUCIBLE_SECRET_KEY") {
		t.Fatalf("the error must say which key is wrong: %v", err)
	}
}

// Ciphertext that was tampered with must not open: GCM authenticates it.
func TestTamperedCiphertextIsRefused(t *testing.T) {
	b := box(t, key)
	sealed, _ := b.Seal("ghp_token")
	bad := sealed[:len(sealed)-2] + "AA"
	if _, err := b.Open(bad); err == nil {
		t.Fatal("a tampered secret opened")
	}
	for _, s := range []string{"not-sealed-at-all", "v1:not base64", "v1:AAAA"} {
		if _, err := b.Open(s); err == nil {
			t.Fatalf("%q opened", s)
		}
	}
}

func TestShortKeyIsRefused(t *testing.T) {
	for _, pass := range []string{"", "   ", "short", strings.Repeat(" ", 20) + "x"} {
		if _, err := New(pass); err == nil {
			t.Fatalf("accepted a weak key %q", pass)
		}
	}
}
