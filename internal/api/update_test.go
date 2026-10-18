package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/onurbaydas/virtomir/internal/activity"
	"github.com/onurbaydas/virtomir/internal/auth"
	"github.com/onurbaydas/virtomir/internal/update"
	"github.com/onurbaydas/virtomir/internal/virt"
)

func TestUpdateRoutes(t *testing.T) {
	m := auth.New(stubAuthn{}, func(u string) auth.Role {
		return map[string]auth.Role{"onur": auth.Admin, "view": auth.Viewer}[u]
	})
	tokens, _ := auth.OpenTokens("")
	actLog, _ := activity.Open("")
	updater := update.New("owner/repo", "0.7.1", "")
	updater.InitConfig(t.TempDir())

	opts := Options{
		Backend:  &fakeBackend{domains: []virt.Domain{}},
		Live:     &fakeLive{},
		Auth:     m,
		Tokens:   tokens,
		Activity: actLog,
		Updater:  updater,
		StateDir: t.TempDir(),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	h := NewServer(opts).Handler()
	adminCookie := signIn(t, h, "onur")
	viewCookie := signIn(t, h, "view")

	// 1. Viewer can read status
	rec := do(h, "GET", "/api/v1/update", "", viewCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/update: code %d, body %s", rec.Code, rec.Body.String())
	}
	var st update.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if !st.Auto.Enabled || st.Auto.IntervalHours != 6 {
		t.Errorf("unexpected auto config in status: %+v", st.Auto)
	}

	// 2. Viewer cannot change auto-update config (Admin required)
	body := `{"enabled":true,"autoInstall":true,"intervalHours":12}`
	rec = do(h, "POST", "/api/v1/update/auto", body, viewCookie)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for viewer, got %d", rec.Code)
	}

	// 3. Admin can change auto-update config
	rec = do(h, "POST", "/api/v1/update/auto", body, adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/update/auto as admin: code %d, body %s", rec.Code, rec.Body.String())
	}
	var updated update.AutoConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal updated config: %v", err)
	}
	if !updated.AutoInstall || updated.IntervalHours != 12 {
		t.Errorf("expected updated config, got: %+v", updated)
	}

	// 4. GET /api/v1/update now reflects updated configuration
	rec = do(h, "GET", "/api/v1/update", "", viewCookie)
	var stAfter update.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &stAfter); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if !stAfter.Auto.AutoInstall || stAfter.Auto.IntervalHours != 12 {
		t.Errorf("GET /api/v1/update after change: %+v", stAfter.Auto)
	}
}
