package activity

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatRFC5424(t *testing.T) {
	entry := Entry{
		Time:   time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
		User:   "admin",
		Remote: "192.168.1.50",
		Method: "POST",
		Path:   "/api/v1/domains/uuid-1/actions/start",
		Status: 200,
	}

	cfg := SIEMConfig{
		Enabled:  true,
		Facility: 16, // local0
		Tag:      "virtomir",
		Hostname: "kvm-host-01",
	}

	msg := FormatRFC5424(entry, cfg)

	// Facility 16 * 8 + Severity 6 (Info) = 134
	if !strings.HasPrefix(msg, "<134>1 ") {
		t.Errorf("expected <134>1 prefix, got %q", msg)
	}

	if !strings.Contains(msg, "kvm-host-01 virtomir - audit") {
		t.Errorf("expected header components, got %q", msg)
	}

	if !strings.Contains(msg, `[audit@virtomir user="admin" remote="192.168.1.50" method="POST" path="/api/v1/domains/uuid-1/actions/start" status="200"]`) {
		t.Errorf("expected structured data, got %q", msg)
	}

	// Test Error status (403) -> Severity 3 (Error) -> 16 * 8 + 3 = 131
	entryFail := Entry{
		Time:   entry.Time,
		User:   "operator",
		Method: "DELETE",
		Path:   "/api/v1/domains/uuid-2",
		Status: 403,
		Error:  "permission denied",
	}
	msgFail := FormatRFC5424(entryFail, cfg)
	if !strings.HasPrefix(msgFail, "<131>1 ") {
		t.Errorf("expected <131>1 prefix for failed action, got %q", msgFail)
	}
	if !strings.Contains(msgFail, "(permission denied)") {
		t.Errorf("expected error in message, got %q", msgFail)
	}
}

func TestForwarderUDPDelivery(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen UDP: %v", err)
	}
	defer pc.Close()

	addr := pc.LocalAddr().String()

	cfg := SIEMConfig{
		Enabled:  true,
		Target:   "udp://" + addr,
		Facility: 1,
		Tag:      "virtomir",
		Hostname: "node-test",
	}

	f := NewForwarder(cfg)
	defer f.Close()

	entry := Entry{
		User:   "devops",
		Method: "PUT",
		Path:   "/api/v1/domains/uuid-3/disks/vda/qos",
		Status: 204,
	}

	f.Send(entry)

	buf := make([]byte, 2048)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("did not receive syslog packet: %v", err)
	}

	got := string(buf[:n])
	if !strings.Contains(got, "node-test virtomir") || !strings.Contains(got, `method="PUT"`) {
		t.Errorf("received payload does not match expected syslog frame: %q", got)
	}
}

func TestSIEMConfigSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "siem.json")
	if _, ok, err := LoadSIEMConfig(path); ok || err != nil {
		t.Fatalf("missing file: ok = %v, err = %v", ok, err)
	}
	want := SIEMConfig{Enabled: true, Target: "tcp://siem.example:514", Facility: 16}
	if err := want.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := SaveSIEMConfig(path, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := LoadSIEMConfig(path)
	if !ok || err != nil || got.Target != want.Target || got.Facility != 16 || !got.Enabled {
		t.Errorf("loaded = %+v, ok = %v, err = %v", got, ok, err)
	}
	for _, bad := range []string{"siem.example:514", "http://siem.example:514", "udp://siem.example"} {
		if (SIEMConfig{Enabled: true, Target: bad}).Validate() == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
