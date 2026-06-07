package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEncryptTokens_RoundTrip(t *testing.T) {
	plain := []byte(`{"gem1":"YWJjZA==","gem2":"ZWZnaA=="}`)
	enc, err := encryptTokens(plain, "correct horse battery staple")
	if err != nil {
		t.Fatalf("encryptTokens: %v", err)
	}
	if enc.KDF != "pbkdf2-sha256" || enc.Iter != pbkdf2Iters || enc.Salt == "" || enc.Nonce == "" || enc.Ciphertext == "" {
		t.Fatalf("encrypted envelope incomplete: %+v", enc)
	}
	got, err := decryptTokens(enc, "correct horse battery staple")
	if err != nil {
		t.Fatalf("decryptTokens: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestDecryptTokens_WrongPassphrase(t *testing.T) {
	enc, err := encryptTokens([]byte("secret"), "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptTokens(enc, "wrong"); err == nil {
		t.Fatal("decrypt with wrong passphrase must fail")
	}
}

func TestDecryptTokens_TamperedCiphertext(t *testing.T) {
	enc, err := encryptTokens([]byte("secret"), "pw")
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte of the base64 ciphertext.
	b := []byte(enc.Ciphertext)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	enc.Ciphertext = string(b)
	if _, err := decryptTokens(enc, "pw"); err == nil {
		t.Fatal("decrypt of tampered ciphertext must fail (GCM auth)")
	}
}

// Two exports of the same plaintext must differ (random salt + nonce), so the
// ciphertext never leaks equality of inputs across bundles.
func TestEncryptTokens_FreshSaltNoncePerExport(t *testing.T) {
	a, _ := encryptTokens([]byte("x"), "pw")
	b, _ := encryptTokens([]byte("x"), "pw")
	if a.Salt == b.Salt || a.Nonce == b.Nonce || a.Ciphertext == b.Ciphertext {
		t.Fatal("salt/nonce/ciphertext must be unique per export")
	}
}

// A v1 bundle round-trips through JSON unchanged on the fields import reads.
func TestExportBundle_JSONRoundTrip(t *testing.T) {
	in := exportBundle{
		Version:  bundleVersion,
		Settings: map[string]string{"auto_swap.enabled": "false"},
		Accounts: []exportAccount{{Label: "gem1", Email: "a@b.co"}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out exportBundle
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Version != bundleVersion || out.Settings["auto_swap.enabled"] != "false" ||
		len(out.Accounts) != 1 || out.Accounts[0].Label != "gem1" {
		t.Fatalf("round-trip altered bundle: %+v", out)
	}
	// No tokens → omitempty keeps the field out of the JSON entirely.
	if bytes.Contains(raw, []byte(`"tokens"`)) {
		t.Fatal("token-less bundle must omit the tokens field")
	}
}
