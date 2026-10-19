package compliance

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDetectKernelFIPS(t *testing.T) {
	// Missing file in empty temp dir
	tmp := t.TempDir()
	if DetectKernelFIPS(tmp) {
		t.Fatal("expected false for missing file")
	}

	// Setup /proc/sys/crypto
	cryptoDir := filepath.Join(tmp, "proc", "sys", "crypto")
	if err := os.MkdirAll(cryptoDir, 0o755); err != nil {
		t.Fatalf("failed to create crypto dir: %v", err)
	}
	fipsPath := filepath.Join(cryptoDir, "fips_enabled")

	// "1" -> true
	if err := os.WriteFile(fipsPath, []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DetectKernelFIPS(tmp) {
		t.Fatal("expected true for '1'")
	}

	// "1\n" -> true
	if err := os.WriteFile(fipsPath, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DetectKernelFIPS(tmp) {
		t.Fatal("expected true for '1\\n'")
	}

	// "0" -> false
	if err := os.WriteFile(fipsPath, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if DetectKernelFIPS(tmp) {
		t.Fatal("expected false for '0'")
	}

	// "0\n" -> false
	if err := os.WriteFile(fipsPath, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if DetectKernelFIPS(tmp) {
		t.Fatal("expected false for '0\\n'")
	}

	// Read error (e.g. directory instead of file): fail-open returning false
	_ = os.Remove(fipsPath)
	if err := os.Mkdir(fipsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if DetectKernelFIPS(tmp) {
		t.Fatal("expected false when reading directory as file (fail-open)")
	}
}

func TestApplyFIPSTLS(t *testing.T) {
	cfg := &tls.Config{}
	ApplyFIPSTLS(cfg)

	// MinVersion is TLS12
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected MinVersion TLS12, got %v", cfg.MinVersion)
	}

	// CipherSuites contains only AES-GCM suites
	if len(cfg.CipherSuites) == 0 {
		t.Fatal("expected non-empty CipherSuites")
	}
	for _, id := range cfg.CipherSuites {
		name := tls.CipherSuiteName(id)
		if !strings.Contains(name, "AES_128_GCM") && !strings.Contains(name, "AES_256_GCM") {
			t.Fatalf("cipher suite %s is not an AES-GCM suite", name)
		}
	}

	// Curves match NIST curves
	expectedCurves := []tls.CurveID{tls.CurveP256, tls.CurveP384, tls.CurveP521}
	if !reflect.DeepEqual(cfg.CurvePreferences, expectedCurves) {
		t.Fatalf("expected curves %v, got %v", expectedCurves, cfg.CurvePreferences)
	}
}

func TestTLSServerHandshake(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	ts.TLS = &tls.Config{}
	ApplyFIPSTLS(ts.TLS)
	ts.StartTLS()
	defer ts.Close()

	// 1. Client requesting TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 connects successfully
	trValid := ts.Client().Transport.(*http.Transport).Clone()
	trValid.TLSClientConfig.MinVersion = tls.VersionTLS12
	trValid.TLSClientConfig.MaxVersion = tls.VersionTLS12
	trValid.TLSClientConfig.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
	clientValid := &http.Client{Transport: trValid}

	resp, err := clientValid.Get(ts.URL)
	if err != nil {
		t.Fatalf("FIPS client handshake failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	// 2. Client offering only non-FIPS ciphers fails / is rejected
	// 2a. ChaCha20-Poly1305
	trChaCha := ts.Client().Transport.(*http.Transport).Clone()
	trChaCha.TLSClientConfig.MinVersion = tls.VersionTLS12
	trChaCha.TLSClientConfig.MaxVersion = tls.VersionTLS12
	trChaCha.TLSClientConfig.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256}
	clientChaCha := &http.Client{Transport: trChaCha}

	if resp, err := clientChaCha.Get(ts.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected handshake failure with ChaCha20-Poly1305, but request succeeded")
	}

	// 2b. CBC cipher (e.g. TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA)
	trCBC := ts.Client().Transport.(*http.Transport).Clone()
	trCBC.TLSClientConfig.MinVersion = tls.VersionTLS12
	trCBC.TLSClientConfig.MaxVersion = tls.VersionTLS12
	trCBC.TLSClientConfig.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA}
	clientCBC := &http.Client{Transport: trCBC}

	if resp, err := clientCBC.Get(ts.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected handshake failure with CBC cipher suite, but request succeeded")
	}
}

func TestGetFIPSInfo(t *testing.T) {
	tmp := t.TempDir()
	cryptoDir := filepath.Join(tmp, "proc", "sys", "crypto")
	if err := os.MkdirAll(cryptoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cryptoDir, "fips_enabled"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	info := GetFIPSInfo(tmp, true)
	if !info.KernelEnabled {
		t.Fatal("expected KernelEnabled true")
	}
	if !info.Enforced {
		t.Fatal("expected Enforced true")
	}
	if info.TLSMinVersion != "TLS 1.2" {
		t.Fatalf("expected TLSMinVersion 'TLS 1.2', got %q", info.TLSMinVersion)
	}
	if len(info.CipherSuites) == 0 {
		t.Fatal("expected non-empty CipherSuites")
	}
	if len(info.Curves) == 0 {
		t.Fatal("expected non-empty Curves")
	}
	if !strings.Contains(info.Description, "experimental, not tested on hardware") {
		t.Fatalf("expected experimental notice in description, got %q", info.Description)
	}

	infoDisabled := GetFIPSInfo(tmp, false)
	if infoDisabled.Enforced {
		t.Fatal("expected Enforced false")
	}
	if infoDisabled.TLSMinVersion != "" {
		t.Fatalf("expected empty TLSMinVersion when not enforced, got %q", infoDisabled.TLSMinVersion)
	}
	if len(infoDisabled.CipherSuites) != 0 {
		t.Fatalf("expected empty CipherSuites when not enforced, got %v", infoDisabled.CipherSuites)
	}
	if len(infoDisabled.Curves) != 0 {
		t.Fatalf("expected empty Curves when not enforced, got %v", infoDisabled.Curves)
	}
	if !strings.Contains(infoDisabled.Description, "experimental, not tested on hardware") {
		t.Fatalf("expected experimental notice in disabled description, got %q", infoDisabled.Description)
	}
}
