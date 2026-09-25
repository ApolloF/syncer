package syncthing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSignedCert returns a throwaway cert's DER bytes and its PEM encoding,
// standing in for Syncthing's own https-cert.pem.
func selfSignedCert(t *testing.T) (der, pemBytes []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "syncthing"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"":                 "127.0.0.1:8384",
		"0.0.0.0:8384":     "127.0.0.1:8384",
		"0.0.0.0":          "127.0.0.1:8384",
		"::":               "127.0.0.1:8384",
		"[::]:8384":        "127.0.0.1:8384",
		"127.0.0.1:9999":   "127.0.0.1:9999",
		"192.168.1.5:8384": "192.168.1.5:8384",
		"localhost:8384":   "localhost:8384",
	}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	yes := []string{"127.0.0.1", "127.5.5.5", "::1", "localhost", "LOCALHOST"}
	no := []string{"192.168.1.5", "10.0.0.1", "example.com", "::2"}
	for _, h := range yes {
		if !isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range no {
		if isLoopbackHost(h) {
			t.Errorf("isLoopbackHost(%q) = true, want false", h)
		}
	}
}

func TestVerifyPinnedCert(t *testing.T) {
	der1, _ := selfSignedCert(t)
	der2, _ := selfSignedCert(t)
	verify := verifyPinnedCert(der1)
	if err := verify([][]byte{der1}, nil); err != nil {
		t.Errorf("matching cert rejected: %v", err)
	}
	if err := verify([][]byte{der2}, nil); err == nil {
		t.Error("mismatched cert accepted")
	}
	if err := verify(nil, nil); err == nil {
		t.Error("empty chain accepted")
	}
}

func TestCertPinnedTransport(t *testing.T) {
	withCert := t.TempDir()
	_, pemBytes := selfSignedCert(t)
	if err := os.WriteFile(filepath.Join(withCert, "https-cert.pem"), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()

	if tr, err := certPinnedTransport(false, true, withCert); err != nil || tr != http.DefaultTransport {
		t.Errorf("TLS off: tr=%v err=%v, want http.DefaultTransport, nil", tr, err)
	}
	if _, err := certPinnedTransport(true, true, withCert); err != nil {
		t.Errorf("TLS on, pem present, loopback: %v", err)
	}
	if _, err := certPinnedTransport(true, false, withCert); err != nil {
		t.Errorf("TLS on, pem present, non-loopback: %v", err)
	}
	if _, err := certPinnedTransport(true, true, empty); err != nil {
		t.Errorf("TLS on, pem missing, loopback should fall back, got: %v", err)
	}
	if _, err := certPinnedTransport(true, false, empty); err == nil {
		t.Error("TLS on, pem missing, non-loopback: want error, got nil")
	}
}
