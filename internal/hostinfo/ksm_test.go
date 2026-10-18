package hostinfo

import (
	"testing"
)

func TestKSMReader(t *testing.T) {
	root := tree(t, map[string]string{
		"sys/kernel/mm/ksm/run":             "1",
		"sys/kernel/mm/ksm/pages_sharing":   "5000",
		"sys/kernel/mm/ksm/pages_shared":    "1200",
		"sys/kernel/mm/ksm/pages_unshared":  "300",
		"sys/kernel/mm/ksm/pages_volatile":  "50",
		"sys/kernel/mm/ksm/sleep_millisecs": "20",
		"sys/kernel/mm/ksm/pages_to_scan":   "100",
	})
	r := &Reader{Root: root}

	info := r.KSM()
	if !info.Supported {
		t.Fatal("expected KSM to be supported")
	}
	if !info.Active {
		t.Errorf("expected Active=true, got false")
	}
	if info.PagesSharing != 5000 || info.PagesShared != 1200 || info.PagesUnshared != 300 || info.PagesVolatile != 50 {
		t.Errorf("unexpected page stats: %+v", info)
	}
	if info.SavedBytes == 0 {
		t.Errorf("expected SavedBytes > 0, got 0")
	}
	if info.SleepMillisecs != 20 || info.PagesToScan != 100 {
		t.Errorf("unexpected tune: sleep=%d, scan=%d", info.SleepMillisecs, info.PagesToScan)
	}

	// Test SetKSM
	if err := r.SetKSM(false); err != nil {
		t.Fatalf("SetKSM failed: %v", err)
	}
	infoAfter := r.KSM()
	if infoAfter.Active {
		t.Errorf("expected Active=false after SetKSM(false)")
	}
}

func TestKSMUnsupported(t *testing.T) {
	root := t.TempDir()
	r := &Reader{Root: root}
	info := r.KSM()
	if info.Supported {
		t.Errorf("expected Supported=false when /sys/kernel/mm/ksm is missing")
	}
}
