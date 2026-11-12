package service

import (
	"errors"
	"testing"
)

func TestDNSKeyEncryptionRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	enc, err := encryptWithKey(key, "pdns-secret")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "pdns-secret" || len(enc) < 24 {
		t.Fatalf("not encrypted: %q", enc)
	}
	// A previous key still decrypts after rotation; an unrelated key does not.
	p := &SettingsDNSProvider{key: []byte("ffffffffffffffffffffffffffffffff"), prevKeys: [][]byte{key}}
	if got, err := p.decrypt(enc); err != nil || got != "pdns-secret" {
		t.Fatalf("previous key should decrypt: %q %v", got, err)
	}
	p.prevKeys = nil
	if _, err := p.decrypt(enc); err == nil {
		t.Fatal("an unrelated key must not decrypt")
	}
}

func TestNormalizeDNSAPIURL(t *testing.T) {
	for _, bad := range []string{"", "pdns.example.com:8081", "ftp://x", "http://", "http://user:pw@host:8081"} {
		if _, _, err := normalizeDNSAPIURL(bad); !errors.Is(err, ErrDNSInvalidURL) {
			t.Errorf("%q should be rejected, got %v", bad, err)
		}
	}
	got, u, err := normalizeDNSAPIURL(" https://pdns.example.com:8081/ ")
	if err != nil || got != "https://pdns.example.com:8081" || u.Host != "pdns.example.com:8081" {
		t.Errorf("got %q %v %v", got, u, err)
	}
}
