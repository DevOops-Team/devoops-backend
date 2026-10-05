package guacamole

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type Credential struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Domain   string `json:"domain"`
}
type Issuer struct {
	URL         string
	Key         []byte
	Credentials map[string]Credential
	TTL         time.Duration
}

func New(address, key string, credentials map[string]Credential, ttl time.Duration) (*Issuer, error) {
	u, e := url.Parse(address)
	if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid GUAC_URL")
	}
	k, e := hex.DecodeString(key)
	if e != nil || len(k) != 16 {
		return nil, fmt.Errorf("GUAC_JSON_KEY must be 32 hex characters")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("invalid Guacamole TTL")
	}
	return &Issuer{address, k, credentials, ttl}, nil
}
func (g *Issuer) Issue(user, desktop int64, name, osType, ip string, now time.Time) (string, time.Time, error) {
	cred, ok := g.Credentials[osType]
	if !ok || cred.Username == "" || cred.Password == "" || ip == "" {
		return "", time.Time{}, fmt.Errorf("RDP credentials unavailable")
	}
	expires := now.UTC().Add(g.TTL)
	parameters := map[string]string{"hostname": ip, "port": "3389", "username": cred.Username, "password": cred.Password, "security": "any", "ignore-cert": "true"}
	if cred.Domain != "" {
		parameters["domain"] = cred.Domain
	}
	payload := map[string]any{"username": "user-" + strconv.FormatInt(user, 10), "expires": expires.UnixMilli(), "connections": map[string]any{fmt.Sprintf("desktop-%d", desktop): map[string]any{"protocol": "rdp", "parameters": parameters}}}
	body, e := json.Marshal(payload)
	if e != nil {
		return "", time.Time{}, e
	}
	mac := hmac.New(sha256.New, g.Key)
	_, _ = mac.Write(body)
	plain := append(mac.Sum(nil), body...)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	for range pad {
		plain = append(plain, byte(pad))
	}
	block, e := aes.NewCipher(g.Key)
	if e != nil {
		return "", time.Time{}, e
	}
	encrypted := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(encrypted, plain)
	u, e := url.Parse(g.URL)
	if e != nil {
		return "", time.Time{}, e
	}
	q := u.Query()
	q.Set("data", base64.StdEncoding.EncodeToString(encrypted))
	u.RawQuery = q.Encode()
	return u.String(), expires, nil
}
