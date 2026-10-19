package guacamole

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

const testKey = "00112233445566778899aabbccddeeff"

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, address, key string
		ttl                time.Duration
	}{
		{"relative", "/guacamole", testKey, time.Minute},
		{"scheme", "ftp://example.test", testKey, time.Minute},
		{"malformed", "https://%", testKey, time.Minute},
		{"empty key", "https://example.test", "", time.Minute},
		{"short key", "https://example.test", "0011", time.Minute},
		{"long key", "https://example.test", testKey + "00", time.Minute},
		{"nonhex", "https://example.test", "zz112233445566778899aabbccddeeff", time.Minute},
		{"zero TTL", "https://example.test", testKey, 0},
		{"negative TTL", "https://example.test", testKey, -time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.address, tc.key, nil, tc.ttl); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestIssueRejectsMissingCredentialsAndIP(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds map[string]Credential
		ip    string
	}{
		{"missing OS", nil, "10.0.0.1"},
		{"missing username", map[string]Credential{"WINDOWS": {Password: "secret"}}, "10.0.0.1"},
		{"missing password", map[string]Credential{"WINDOWS": {Username: "rdp"}}, "10.0.0.1"},
		{"missing IP", map[string]Credential{"WINDOWS": {Username: "rdp", Password: "secret"}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer, err := New("https://example.test/guacamole/", testKey, tc.creds, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			token, expires, err := issuer.Issue(7, 11, "vm", "WINDOWS", tc.ip, time.Now())
			if err == nil || token != "" || !expires.IsZero() {
				t.Fatal("invalid credentials must not issue a token")
			}
		})
	}
}

func TestIssueTokenProtocolAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 123000000, time.FixedZone("KST", 9*3600))
	for _, domain := range []string{"", "CORP"} {
		issuer, err := New("https://example.test/guacamole/?keep=yes", testKey, map[string]Credential{"WINDOWS": {Username: "rdp", Password: "test-password", Domain: domain}}, 2*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		token, expires, err := issuer.Issue(7, 11, "vm", "WINDOWS", "10.0.0.1", now)
		if err != nil {
			t.Fatal(err)
		}
		if !expires.Equal(now.Add(2*time.Minute)) || expires.Location() != time.UTC {
			t.Fatal("incorrect expiry")
		}
		u, err := url.Parse(token)
		if err != nil {
			t.Fatal(err)
		}
		if u.Scheme != "https" || u.Host != "example.test" || u.Path != "/guacamole/" || u.Query().Get("keep") != "yes" {
			t.Fatal("URL not preserved")
		}
		encrypted, err := base64.StdEncoding.DecodeString(u.Query().Get("data"))
		if err != nil {
			t.Fatal(err)
		}
		if len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
			t.Fatal("invalid ciphertext length")
		}
		key, _ := hex.DecodeString(testKey)
		block, _ := aes.NewCipher(key)
		plain := make([]byte, len(encrypted))
		cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(plain, encrypted)
		pad := int(plain[len(plain)-1])
		if pad < 1 || pad > aes.BlockSize {
			t.Fatal("invalid padding")
		}
		for _, b := range plain[len(plain)-pad:] {
			if int(b) != pad {
				t.Fatal("invalid padding bytes")
			}
		}
		plain = plain[:len(plain)-pad]
		if len(plain) < sha256.Size {
			t.Fatal("missing signature")
		}
		mac := hmac.New(sha256.New, key)
		mac.Write(plain[sha256.Size:])
		if !hmac.Equal(mac.Sum(nil), plain[:sha256.Size]) {
			t.Fatal("invalid signature")
		}
		var payload struct {
			Username    string `json:"username"`
			Expires     int64  `json:"expires"`
			Connections map[string]struct {
				Protocol   string            `json:"protocol"`
				Parameters map[string]string `json:"parameters"`
			} `json:"connections"`
		}
		if err := json.Unmarshal(plain[sha256.Size:], &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Username != "user-7" || payload.Expires != expires.UnixMilli() || len(payload.Connections) != 1 {
			t.Fatal("incorrect user, expiry or connection scope")
		}
		conn, ok := payload.Connections["desktop-11"]
		if !ok || conn.Protocol != "rdp" {
			t.Fatal("incorrect connection")
		}
		for k, want := range map[string]string{"hostname": "10.0.0.1", "port": "3389", "username": "rdp", "password": "test-password", "security": "any", "ignore-cert": "true"} {
			if conn.Parameters[k] != want {
				t.Fatalf("incorrect %s", k)
			}
		}
		got, present := conn.Parameters["domain"]
		if got != domain || present != (domain != "") {
			t.Fatal("incorrect domain")
		}
	}
}
