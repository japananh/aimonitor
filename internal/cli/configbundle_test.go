package cli

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestEncryptTokens_RoundTrip(t *testing.T) {
	plain := []byte(`{"gem1":"YWJjZA==","gem2":"ZWZnaA=="}`)
	enc, err := encryptTokens(plain, "correct horse battery staple")
	if err != nil {
		t.Fatalf("encryptTokens: %v", err)
	}
	// New exports use Argon2id with the OWASP params, not PBKDF2.
	if enc.KDF != kdfArgon2id || enc.Memory != argon2Memory || enc.Time != argon2Time ||
		enc.Threads != argon2Threads || enc.Salt == "" || enc.Nonce == "" || enc.Ciphertext == "" {
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

// A bundle written by v1.1.1 (PBKDF2) must still decrypt — built here exactly
// as the old code did, then read back through the kdf-aware decryptTokens.
func TestDecryptTokens_PBKDF2Backcompat(t *testing.T) {
	plain := []byte(`{"gem1":"YWJjZA=="}`)
	pass := "old-bundle-pass"
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pass, salt, pbkdf2Iters, 32)
	if err != nil {
		t.Fatalf("pbkdf2: %v", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	_, _ = rand.Read(nonce)
	ct := gcm.Seal(nil, nonce, plain, nil)
	old := &encryptedTokens{
		KDF:        kdfPBKDF2,
		Iter:       pbkdf2Iters,
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ct),
	}
	got, err := decryptTokens(old, pass)
	if err != nil {
		t.Fatalf("decrypt v1.1.1 (pbkdf2) bundle: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("backcompat mismatch: got %q want %q", got, plain)
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
