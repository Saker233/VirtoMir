package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/onurbaydas/virtomir/internal/compliance"
)

func TestGetFIPS(t *testing.T) {
	expected := compliance.FIPSInfo{
		KernelEnabled: true,
		Enforced:      true,
		TLSMinVersion: "TLS 1.2",
		CipherSuites:  []string{"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256"},
		Curves:        []string{"P-256"},
		Description:   "FIPS mode enabled: restricting TLS to FIPS 140 approved cipher suites (experimental, not tested on hardware)",
	}

	ui := fstest.MapFS{
		"index.html": {Data: []byte("<html>app</html>")},
	}
	srv := NewServer(Options{
		Backend:  &fakeBackend{},
		Live:     &fakeLive{},
		UI:       ui,
		Version:  "test",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		FIPSInfo: expected,
	})

	if got := srv.FIPSInfo(); got.Enforced != expected.Enforced || got.KernelEnabled != expected.KernelEnabled {
		t.Fatalf("accessor mismatch: got %+v, want %+v", got, expected)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/compliance/fips", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var res compliance.FIPSInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if !res.Enforced || !res.KernelEnabled {
		t.Fatalf("unexpected fips response: %+v", res)
	}
	if res.TLSMinVersion != "TLS 1.2" {
		t.Fatalf("expected TLS 1.2, got %q", res.TLSMinVersion)
	}
	if len(res.CipherSuites) != 1 || res.CipherSuites[0] != "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256" {
		t.Fatalf("unexpected cipher suites: %v", res.CipherSuites)
	}
}
