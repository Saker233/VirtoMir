// Package compliance provides host hardening posture evaluation (CIS Linux / STIG
// virtualization host subset), FIPS mode detection, and audit verification (ADR-0064).
package compliance

import (
	"crypto/tls"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// FIPSInfo represents the host kernel FIPS state and daemon TLS restriction configuration.
// Evaluation on physical cryptographic hardware is marked experimental.
type FIPSInfo struct {
	KernelEnabled bool     `json:"kernelEnabled"`
	Enforced      bool     `json:"enforced"`
	TLSMinVersion string   `json:"tlsMinVersion"`
	CipherSuites  []string `json:"cipherSuites"`
	Curves        []string `json:"curves"`
	Description   string   `json:"description"`
}

// DetectKernelFIPS reads /proc/sys/crypto/fips_enabled below root and returns true if content is "1" or "1\n".
func DetectKernelFIPS(root string) bool {
	p := filepath.Join(root, "proc", "sys", "crypto", "fips_enabled")
	b, err := os.ReadFile(p)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("failed to read kernel fips_enabled, failing open to non-FIPS mode", "path", p, "err", err)
		}
		return false
	}
	s := strings.TrimSpace(string(b))
	return s == "1"
}

// FIPSCipherSuites returns the FIPS 140-approved cipher suites from crypto/tls.
// (TLS 1.3 ciphers TLS_AES_128_GCM_SHA256 and TLS_AES_256_GCM_SHA384 are also approved).
func FIPSCipherSuites() []uint16 {
	return []uint16{
		tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
		tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	}
}

// FIPSCurves returns the FIPS 140-approved NIST elliptic curves from crypto/tls.
func FIPSCurves() []tls.CurveID {
	return []tls.CurveID{
		tls.CurveP256,
		tls.CurveP384,
		tls.CurveP521,
	}
}

// ApplyFIPSTLS configures a TLS config to enforce FIPS 140 cipher suites, NIST curves, and MinVersion TLS 1.2.
func ApplyFIPSTLS(cfg *tls.Config) {
	if cfg == nil {
		return
	}
	cfg.MinVersion = tls.VersionTLS12
	cfg.CipherSuites = FIPSCipherSuites()
	cfg.CurvePreferences = FIPSCurves()
}

// GetFIPSInfo builds a FIPSInfo status struct with human-readable cipher suite and curve names.
func GetFIPSInfo(root string, enforced bool) FIPSInfo {
	desc := "FIPS mode disabled (experimental, not tested on hardware)"
	var suites []string
	var curves []string
	var minVer string
	if enforced {
		desc = "FIPS mode enabled: restricting TLS to FIPS 140 approved cipher suites (experimental, not tested on hardware)"
		minVer = tls.VersionName(tls.VersionTLS12)
		suites = make([]string, 0, len(FIPSCipherSuites()))
		for _, id := range FIPSCipherSuites() {
			suites = append(suites, tls.CipherSuiteName(id))
		}

		curves = make([]string, 0, len(FIPSCurves()))
		for _, c := range FIPSCurves() {
			curves = append(curves, c.String())
		}
	}

	return FIPSInfo{
		KernelEnabled: DetectKernelFIPS(root),
		Enforced:      enforced,
		TLSMinVersion: minVer,
		CipherSuites:  suites,
		Curves:        curves,
		Description:   desc,
	}
}
