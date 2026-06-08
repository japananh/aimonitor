package cli

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestEncryptTokens_RoundTrip(t *testing.T) {
	plain := []byte(`[{"label":"gem1","token":"YWJjZA=="}]`)
	enc, err := encryptTokens(plain, "correct horse battery staple")
	if err != nil {
		t.Fatalf("encryptTokens: %v", err)
	}
	if enc.Cipher != cipherAES256GCM || enc.KDF.Algorithm != kdfArgon2id ||
		enc.KDF.MemoryKiB != argon2Memory || enc.KDF.Iterations != argon2Time ||
		enc.KDF.Parallelism != argon2Threads || enc.KDF.Salt == "" || enc.Nonce == "" || enc.Data == "" {
		t.Fatalf("encrypted envelope incomplete/not argon2id: %+v", enc)
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
	b := []byte(enc.Data)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	enc.Data = string(b)
	if _, err := decryptTokens(enc, "pw"); err == nil {
		t.Fatal("decrypt of tampered ciphertext must fail (GCM auth)")
	}
}

// Two exports of the same plaintext must differ (random salt + nonce), so the
// ciphertext never leaks equality of inputs across bundles.
func TestEncryptTokens_FreshSaltNoncePerExport(t *testing.T) {
	a, _ := encryptTokens([]byte("x"), "pw")
	b, _ := encryptTokens([]byte("x"), "pw")
	if a.KDF.Salt == b.KDF.Salt || a.Nonce == b.Nonce || a.Data == b.Data {
		t.Fatal("salt/nonce/ciphertext must be unique per export")
	}
}

// encAccount must serialize with the embedded identity fields FLAT (label,
// email, …) alongside token — that's the shape the encrypted payload relies on.
func TestEncAccount_JSONShape(t *testing.T) {
	in := []encAccount{{
		exportAccount: exportAccount{Label: "gem1", Email: "a@b.co", OrganizationUUID: "u1", OrganizationName: "Org"},
		Token:         "dG9rZW4=",
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out []encAccount
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Label != "gem1" || out[0].Email != "a@b.co" ||
		out[0].OrganizationUUID != "u1" || out[0].OrganizationName != "Org" || out[0].Token != "dG9rZW4=" {
		t.Fatalf("encAccount round-trip altered record: %+v", out)
	}
}

// The no-token bundle keeps plaintext identities; a token bundle omits them.
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
	if bytes.Contains(raw, []byte(`"tokens"`)) {
		t.Fatal("token-less bundle must omit the tokens field")
	}
}
