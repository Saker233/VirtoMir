package insights

import (
	"math"
	"testing"
	"time"

	"github.com/onurbaydas/virtomir/internal/history"
)

func newTestEngine(t *testing.T) (*Engine, *history.Store) {
	t.Helper()
	store := history.New()
	return New(store), store
}

func seedLinearRamp(t *testing.T, store *history.Store, name string, startV, slopePerDay float64, from, to time.Time, step time.Duration) {
	t.Helper()
	for t2 := from; !t2.After(to); t2 = t2.Add(step) {
		day := t2.Sub(from).Hours() / 24.0
		v := startV + slopePerDay*day
		store.Add(name, t2, v)
	}
}

func TestHostForecast_SyntheticRamp(t *testing.T) {
	eng, store := newTestEngine(t)
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	from := now.Add(-30 * 24 * time.Hour)
	// CPU ramps 20% -> 80% at 2 %/day; Mem ramps 40% -> 85% at ~1.43 %/day
	seedLinearRamp(t, store, "host.cpu", 20.0, 2.0, from, now, time.Hour)
	seedLinearRamp(t, store, "host.mem", 40.0, 1.238, from, now, time.Hour)
	seedLinearRamp(t, store, "host.disk.read", 1e8, 1e6, from, now, time.Hour)
	seedLinearRamp(t, store, "host.disk.write", 5e7, 5e5, from, now, time.Hour)

	cpu, mem, disk, err := eng.HostForecast(HostCap{ID: "h1", Name: "test", CPUs: 8, MemMiB: 16384}, now)
	if err != nil {
		t.Fatalf("HostForecast error: %v", err)
	}
	if cpu.InsufficientData {
		t.Fatalf("expected CPU InsufficientData=false, got true")
	}
	if cpu.Slope <= 0 {
		t.Errorf("expected positive CPU slope, got %f", cpu.Slope)
	}
	if cpu.DaysUntilFull <= 0 {
		t.Errorf("expected positive DaysUntilFull for CPU, got %f", cpu.DaysUntilFull)
	}
	// Slope should be close to 2.0 %/day
	if math.Abs(cpu.Slope-2.0) > 0.3 {
		t.Errorf("expected CPU slope ~2.0, got %f", cpu.Slope)
	}

	if mem.Slope <= 0 {
		t.Errorf("expected positive Mem slope, got %f", mem.Slope)
	}
	if mem.DaysUntilFull <= 0 {
		t.Errorf("expected positive DaysUntilFull for Mem, got %f", mem.DaysUntilFull)
	}

	if len(disk) != 2 {
		t.Fatalf("expected 2 disk forecasts, got %d", len(disk))
	}
	for _, name := range []string{"host.disk.read", "host.disk.write"} {
		f, ok := disk[name]
		if !ok {
			t.Errorf("missing disk forecast for %s", name)
		}
		if f.Slope <= 0 {
			t.Errorf("expected positive slope for %s, got %f", name, f.Slope)
		}
	}
}

func TestClusterForecast_SyntheticRamp(t *testing.T) {
	eng, store := newTestEngine(t)
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	from := now.Add(-30 * 24 * time.Hour)
	// CPU ramps 20% -> 80% at 2 %/day
	seedLinearRamp(t, store, "host.cpu", 20.0, 2.0, from, now, time.Hour)
	seedLinearRamp(t, store, "host.mem", 40.0, 1.0, from, now, time.Hour)

	hosts := []HostCap{
		{ID: "", Name: "localhost", CPUs: 8, MemMiB: 16384},
	}
	cpu, mem, err := eng.ClusterForecast(hosts, now)
	if err != nil {
		t.Fatalf("ClusterForecast error: %v", err)
	}
	if cpu.InsufficientData {
		t.Fatalf("expected cluster CPU InsufficientData=false, got true")
	}
	if cpu.Slope <= 0 {
		t.Errorf("expected positive cluster CPU slope, got %f", cpu.Slope)
	}
	if cpu.DaysUntilFull <= 0 {
		t.Errorf("expected positive DaysUntilFull, got %f", cpu.DaysUntilFull)
	}
	if memorySlope := mem.Slope; memorySlope <= 0 {
		t.Errorf("expected positive cluster Mem slope, got %f", memorySlope)
	}
	if mem.DaysUntilFull <= 0 {
		t.Errorf("expected positive DaysUntilFull for Mem, got %f", mem.DaysUntilFull)
	}
}

func TestVMRightSize_Oversized(t *testing.T) {
	eng, store := newTestEngine(t)
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	from := now.Add(-30 * 24 * time.Hour)
	uuid := "vm-test-123"
	// CPU p95 = 10%, p99 = 25%
	seedLinearRamp(t, store, vmSeries(uuid, "cpu"), 10.0, 0, from, now, time.Hour)
	// Memory p95 = 20%
	seedLinearRamp(t, store, vmSeries(uuid, "mem"), 20.0, 0, from, now, time.Hour)

	m := Machine{UUID: uuid, Name: "test-vm", VCPUs: 8, MemMiB: 4096, Running: true}
	rec := eng.VMRightSize(m, 0, now)

	if !rec.Oversized {
		t.Errorf("expected Oversized=true, got false")
	}
	if rec.SuggestedVCPUs >= m.VCPUs {
		t.Errorf("expected suggested vCPUs < current (%d), got %d", m.VCPUs, rec.SuggestedVCPUs)
	}
	if rec.SuggestedMemMiB >= m.MemMiB {
		t.Errorf("expected suggested MemMiB < current (%d), got %d", m.MemMiB, rec.SuggestedMemMiB)
	}
}

func TestVMRightSize_NoGuestMemory(t *testing.T) {
	eng, store := newTestEngine(t)
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	from := now.Add(-30 * 24 * time.Hour)
	uuid := "vm-no-ballon"
	// Only CPU series; no guest memory series
	seedLinearRamp(t, store, vmSeries(uuid, "cpu"), 10.0, 0, from, now, time.Hour)

	m := Machine{UUID: uuid, Name: "no-balloon-vm", VCPUs: 4, MemMiB: 2048, Running: true}
	rec := eng.VMRightSize(m, 0, now)

	if rec.CurrentMemMiB != m.MemMiB {
		t.Errorf("expected CurrentMemMiB unchanged (%d), got %d", m.MemMiB, rec.CurrentMemMiB)
	}
	if rec.SuggestedMemMiB != m.MemMiB {
		t.Errorf("expected SuggestedMemMiB unchanged when no guest memory (%d), got %d", m.MemMiB, rec.SuggestedMemMiB)
	}
	found := false
	for _, r := range rec.Reasons {
		if r == "guest agent memory data unavailable" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'guest agent memory data unavailable' in reasons, got %v", rec.Reasons)
	}
}

func TestVMDetect_DiskLatencySpike(t *testing.T) {
	eng, store := newTestEngine(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	from := now.Add(-24 * time.Hour)
	uuid := "vm-detect-test"
	// 144 points at 10-minute intervals, baseline ~10ms, with a 10x spike (100ms)
	tSpike := now.Add(-2 * time.Hour)
	for i := 0; i < 144; i++ {
		curT := from.Add(time.Duration(i) * 10 * time.Minute)
		v := 10.0
		if curT.Equal(tSpike) {
			v = 100.0
		}
		store.Add(vmSeries(uuid, "disk.latency"), curT, v)
	}
	// Seed the other 5 series with flat low values so VMDetect doesn't error
	for _, metric := range []string{"cpu", "disk.read", "disk.write", "net.rx", "net.tx"} {
		seedLinearRamp(t, store, vmSeries(uuid, metric), 5.0, 0, from, now, 10*time.Minute)
	}

	m := Machine{UUID: uuid, Name: "spike-vm", VCPUs: 2, MemMiB: 1024, Running: true}
	anomaliesMap := eng.VMDetect(m, now)

	lats, ok := anomaliesMap["disk.latency"]
	if !ok {
		t.Fatalf("expected anomalies for disk.latency, got %+v", anomaliesMap)
	}
	if len(lats) != 1 {
		t.Fatalf("expected exactly 1 anomaly for disk.latency, got %d", len(lats))
	}
	if lats[0].Z <= 5.0 {
		t.Errorf("expected Z > 5.0, got %f", lats[0].Z)
	}
}

func TestPoolForecast_NoPoolData(t *testing.T) {
	eng, _ := newTestEngine(t)
	now := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	f, err := eng.PoolForecast(Pool{Name: "default-pool", CapacityBytes: 1024 * 1024 * 1024, FreeBytes: 512 * 1024 * 1024}, now)
	if err != nil {
		t.Fatalf("PoolForecast error: %v", err)
	}
	if !f.InsufficientData {
		t.Errorf("expected InsufficientData=true (no pool series recorded), got false")
	}
	if f.DaysUntilFull != -1 {
		t.Errorf("expected DaysUntilFull=-1, got %f", f.DaysUntilFull)
	}
}
