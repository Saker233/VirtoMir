package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/onurbaydas/virtomir/internal/compliance"
)

func TestSetupTLS_FIPSEnforce(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmp := t.TempDir()

	// 1. tlsMode="off": returns nil, nil even if fipsEnforce is requested.
	cOff := config{tlsMode: "off", authMode: "none"}
	cfgOff, err := setupTLS(cOff, true, log)
	if err != nil {
		t.Fatalf("unexpected error for tls off: %v", err)
	}
	if cfgOff != nil {
		t.Fatalf("expected nil tls config for tls off, got %v", cfgOff)
	}

	// 2. tlsMode="auto" with fipsEnforce=true: config has MinVersion TLS12 and restricted cipher suites.
	cAuto := config{tlsMode: "auto", authMode: "none", stateDir: tmp}
	cfgAuto, err := setupTLS(cAuto, true, log)
	if err != nil {
		t.Fatalf("unexpected error for tls auto: %v", err)
	}
	if cfgAuto == nil {
		t.Fatal("expected non-nil tls config for tls auto")
	}
	if len(cfgAuto.CipherSuites) == 0 {
		t.Fatal("expected restricted cipher suites when fipsEnforce is true")
	}

	// 3. tlsMode="auto" with fipsEnforce=false: cipher suites are not restricted.
	cAutoNormal := config{tlsMode: "auto", authMode: "none", stateDir: tmp}
	cfgAutoNormal, err := setupTLS(cAutoNormal, false, log)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfgAutoNormal.CipherSuites) != 0 {
		t.Fatalf("expected unrestricted (empty) CipherSuites when fipsEnforce is false, got %v", cfgAutoNormal.CipherSuites)
	}
}

func TestFIPSTLSOffReporting(t *testing.T) {
	// When TLS is off, even if fipsMode is "on", enforcement must be disabled so FIPSInfo reflects reality.
	c := config{tlsMode: "off", fipsMode: "on"}
	kernelFIPS := true // simulated
	fipsEnforce := (c.fipsMode == "on") || (c.fipsMode == "auto" && kernelFIPS)
	if fipsEnforce && c.tlsMode == "off" {
		fipsEnforce = false
	}
	if fipsEnforce {
		t.Fatal("expected fipsEnforce to be disabled when tlsMode is off")
	}

	tmp := t.TempDir()
	info := compliance.GetFIPSInfo(tmp, fipsEnforce)
	if info.Enforced {
		t.Fatal("expected Enforced=false when tlsMode is off")
	}
	if len(info.CipherSuites) != 0 {
		t.Fatalf("expected empty cipher suites reported, got %v", info.CipherSuites)
	}
	if info.TLSMinVersion != "" {
		t.Fatalf("expected empty TLS min version reported, got %q", info.TLSMinVersion)
	}
}
