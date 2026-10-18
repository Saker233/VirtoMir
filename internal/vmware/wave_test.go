package vmware

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWaveConverter struct {
	mu          sync.Mutex
	running     atomic.Int32
	maxParallel atomic.Int32
	calls       map[string]int
	fail        map[string]error
	delay       time.Duration
}

func newFakeWaveConverter(delay time.Duration) *fakeWaveConverter {
	return &fakeWaveConverter{
		calls: make(map[string]int),
		fail:  make(map[string]error),
		delay: delay,
	}
}

func (f *fakeWaveConverter) ConvertOne(ctx context.Context, conn Connection, vmID string, report func(string)) error {
	cur := f.running.Add(1)
	defer f.running.Add(-1)

	for {
		old := f.maxParallel.Load()
		if cur <= old || f.maxParallel.CompareAndSwap(old, cur) {
			break
		}
	}

	f.mu.Lock()
	f.calls[vmID]++
	err := f.fail[vmID]
	f.mu.Unlock()

	if report != nil {
		report("Converting " + vmID)
	}

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return err
}

func (f *fakeWaveConverter) callCount(vmID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[vmID]
}

// with parallel=2 and 5 items at most 2 run concurrently (track with an atomic counter).
func TestWaveConcurrencyLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(30 * time.Millisecond)
	vms := []string{"vm1", "vm2", "vm3", "vm4", "vm5"}

	var reports []string
	var repMu sync.Mutex
	report := func(msg string) {
		repMu.Lock()
		reports = append(reports, msg)
		repMu.Unlock()
	}

	w, err := reg.Start(ctx, Connection{}, vms, 2, fake, report)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if w.Parallel != 2 {
		t.Errorf("Parallel = %d, want 2", w.Parallel)
	}
	if max := fake.maxParallel.Load(); max != 2 {
		t.Errorf("max concurrent = %d, want exactly 2", max)
	}
	if len(w.Items) != 5 {
		t.Fatalf("len(Items) = %d, want 5", len(w.Items))
	}
	for _, it := range w.Items {
		if it.State != WaveItemDone {
			t.Errorf("item %s state = %q, want done", it.VMID, it.State)
		}
	}

	repMu.Lock()
	lastReport := reports[len(reports)-1]
	repMu.Unlock()
	if lastReport != "5 of 5 done" {
		t.Errorf("last report = %q, want %q", lastReport, "5 of 5 done")
	}
}

// a failing item does not cancel the others; partial failure leaves the wave done-with-failures
// and the job report mentions counts.
func TestWavePartialFailureDoesNotCancelOthers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(20 * time.Millisecond)
	fake.fail["vm3"] = errors.New("disk read error on datastore")

	vms := []string{"vm1", "vm2", "vm3", "vm4", "vm5"}

	var reports []string
	var repMu sync.Mutex
	report := func(msg string) {
		repMu.Lock()
		reports = append(reports, msg)
		repMu.Unlock()
	}

	w, err := reg.Start(ctx, Connection{}, vms, 2, fake, report)
	if err != nil {
		t.Fatalf("partial failure should succeed the job, got err: %v", err)
	}

	// Verify all items ran
	for _, id := range vms {
		if fake.callCount(id) != 1 {
			t.Errorf("item %s call count = %d, want 1", id, fake.callCount(id))
		}
	}

	// Verify states
	for i, it := range w.Items {
		if it.VMID == "vm3" {
			if it.State != WaveItemFailed {
				t.Errorf("vm3 state = %q, want failed", it.State)
			}
			if it.Message != "disk read error on datastore" {
				t.Errorf("vm3 message = %q, want error text", it.Message)
			}
		} else {
			if it.State != WaveItemDone {
				t.Errorf("item %d (%s) state = %q, want done", i, it.VMID, it.State)
			}
		}
	}

	repMu.Lock()
	lastReport := reports[len(reports)-1]
	repMu.Unlock()
	if lastReport != "4 of 5 done, 1 failed" {
		t.Errorf("final report = %q, want %q", lastReport, "4 of 5 done, 1 failed")
	}
}

// Retry re-runs exactly the failed items and flips them to done.
func TestWaveRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(10 * time.Millisecond)
	fake.fail["vm2"] = errors.New("network timeout")
	fake.fail["vm4"] = errors.New("storage locked")

	vms := []string{"vm1", "vm2", "vm3", "vm4", "vm5"}
	w, err := reg.Start(ctx, Connection{URL: "https://vcenter.test"}, vms, 2, fake, nil)
	if err != nil {
		t.Fatalf("Start = %v", err)
	}

	if fake.callCount("vm2") != 1 || fake.callCount("vm4") != 1 {
		t.Fatalf("unexpected call counts before retry")
	}

	// Clear scripted failures for retry
	fake.mu.Lock()
	delete(fake.fail, "vm2")
	delete(fake.fail, "vm4")
	fake.mu.Unlock()

	var reports []string
	var repMu sync.Mutex
	report := func(msg string) {
		repMu.Lock()
		reports = append(reports, msg)
		repMu.Unlock()
	}

	retried, err := reg.Retry(ctx, w.ID, fake, report)
	if err != nil {
		t.Fatalf("Retry = %v", err)
	}

	// Verify exactly vm2 and vm4 were re-run
	for _, id := range []string{"vm1", "vm3", "vm5"} {
		if cnt := fake.callCount(id); cnt != 1 {
			t.Errorf("%s was re-run (%d calls), want 1", id, cnt)
		}
	}
	for _, id := range []string{"vm2", "vm4"} {
		if cnt := fake.callCount(id); cnt != 2 {
			t.Errorf("%s was not retried (%d calls), want 2", id, cnt)
		}
	}

	// All items must now be done
	for _, it := range retried.Items {
		if it.State != WaveItemDone {
			t.Errorf("item %s state = %q, want done", it.VMID, it.State)
		}
		if it.Message != "" {
			t.Errorf("item %s message = %q, want empty after done", it.VMID, it.Message)
		}
	}

	repMu.Lock()
	lastReport := reports[len(reports)-1]
	repMu.Unlock()
	if lastReport != "5 of 5 done" {
		t.Errorf("final report after retry = %q, want %q", lastReport, "5 of 5 done")
	}
}

// When every item fails, Start and Retry return an error so the jobs framework marks it failed.
func TestWaveAllFailReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(0)
	fake.fail["vm1"] = errors.New("err1")
	fake.fail["vm2"] = errors.New("err2")
	fake.fail["vm3"] = errors.New("err3")

	vms := []string{"vm1", "vm2", "vm3"}

	var lastReport string
	report := func(msg string) { lastReport = msg }

	w, err := reg.Start(ctx, Connection{}, vms, 2, fake, report)
	if err == nil {
		t.Fatal("expected error when all items fail, got nil")
	}

	for _, it := range w.Items {
		if it.State != WaveItemFailed {
			t.Errorf("item %s state = %q, want failed", it.VMID, it.State)
		}
	}
	if lastReport != "0 of 3 done, 3 failed" {
		t.Errorf("last report = %q, want %q", lastReport, "0 of 3 done, 3 failed")
	}

	// Retrying with all failing again should return error too
	_, err = reg.Retry(ctx, w.ID, fake, report)
	if err == nil {
		t.Fatal("expected error when all retried items fail, got nil")
	}
}

// Get/List ordering newest-first.
func TestWaveGetAndListOrdering(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(0)

	w1, err := reg.Start(ctx, Connection{}, []string{"vm1"}, 1, fake, nil)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := reg.Start(ctx, Connection{}, []string{"vm2"}, 1, fake, nil)
	if err != nil {
		t.Fatal(err)
	}
	w3, err := reg.Start(ctx, Connection{}, []string{"vm3"}, 1, fake, nil)
	if err != nil {
		t.Fatal(err)
	}

	list := reg.List()
	if len(list) != 3 {
		t.Fatalf("len(List) = %d, want 3", len(list))
	}
	if list[0].ID != w3.ID || list[1].ID != w2.ID || list[2].ID != w1.ID {
		t.Errorf("list order = [%s, %s, %s], want newest first [%s, %s, %s]",
			list[0].ID, list[1].ID, list[2].ID, w3.ID, w2.ID, w1.ID)
	}

	// Get existing
	got, ok := reg.Get(w2.ID)
	if !ok || got.ID != w2.ID {
		t.Errorf("Get(%q) = (%+v, %v), want w2", w2.ID, got, ok)
	}

	// Get missing
	_, ok = reg.Get("does-not-exist")
	if ok {
		t.Errorf("Get missing = true, want false")
	}
}

// Registry retains at most 20 waves, dropping oldest.
func TestWaveRegistryKeepLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(0)

	var ids []string
	for i := range 25 {
		w, err := reg.Start(ctx, Connection{}, []string{fmt.Sprintf("vm%d", i)}, 1, fake, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, w.ID)
	}

	list := reg.List()
	if len(list) != KeepWaves {
		t.Fatalf("len(List) = %d, want %d", len(list), KeepWaves)
	}

	// Newest should be ids[24], oldest in list should be ids[5]
	if list[0].ID != ids[24] {
		t.Errorf("newest = %s, want %s", list[0].ID, ids[24])
	}
	if list[KeepWaves-1].ID != ids[5] {
		t.Errorf("oldest = %s, want %s", list[KeepWaves-1].ID, ids[5])
	}

	// The first 5 should be gone
	for i := range 5 {
		if _, ok := reg.Get(ids[i]); ok {
			t.Errorf("wave %s still exists, should have been trimmed", ids[i])
		}
	}
}

// Parallel clamping: 1..4, default 2.
func TestWaveParallelClamping(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(0)

	for _, tc := range []struct {
		input int
		want  int
	}{
		{0, 2},
		{-5, 2},
		{1, 1},
		{3, 3},
		{4, 4},
		{10, 4},
	} {
		w, err := reg.Start(ctx, Connection{}, []string{"vm"}, tc.input, fake, nil)
		if err != nil {
			t.Fatal(err)
		}
		if w.Parallel != tc.want {
			t.Errorf("Start(parallel=%d) = %d, want %d", tc.input, w.Parallel, tc.want)
		}
	}
}

// Duplicate IDs are stripped, and empty lists are rejected.
func TestWaveValidationAndDeduplication(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(0)

	// Empty list
	if _, err := reg.Start(ctx, Connection{}, nil, 2, fake, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("Start(nil) = %v, want ErrInvalid", err)
	}
	if _, err := reg.Start(ctx, Connection{}, []string{"", "   "}, 2, fake, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("Start(whitespace) = %v, want ErrInvalid", err)
	}

	// Duplicates
	input := []string{"vm1", "vm2", "vm1", "vm3", "vm2", "  vm4  "}
	w, err := reg.Start(ctx, Connection{}, input, 2, fake, nil)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if len(w.Items) != 4 {
		t.Fatalf("len(w.Items) = %d, want 4 unique", len(w.Items))
	}
	wantIDs := []string{"vm1", "vm2", "vm3", "vm4"}
	for i, want := range wantIDs {
		if w.Items[i].VMID != want {
			t.Errorf("item %d ID = %q, want %q", i, w.Items[i].VMID, want)
		}
		if cnt := fake.callCount(want); cnt != 1 {
			t.Errorf("callCount(%q) = %d, want 1", want, cnt)
		}
	}
}

// Cancelling the context stops pending conversions and marks items failed with context error.
func TestWaveCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())

	reg := NewRegistry()
	fake := newFakeWaveConverter(60 * time.Millisecond)
	vms := []string{"vm1", "vm2", "vm3", "vm4"}

	startErrCh := make(chan error, 1)
	go func() {
		_, err := reg.Start(ctx, Connection{}, vms, 1, fake, nil)
		startErrCh <- err
	}()

	// Let first VM start, then cancel
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-startErrCh:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not terminate after context cancel")
	}

	// Check registry wave state
	waves := reg.List()
	if len(waves) != 1 {
		t.Fatalf("len(waves) = %d, want 1", len(waves))
	}
	w := waves[0]
	for _, item := range w.Items {
		if item.State != WaveItemFailed {
			t.Errorf("item %s state = %q, want failed", item.VMID, item.State)
		}
	}
}

// Mid-run Get and List see current progress; Retry while running is rejected.
func TestWaveMidRunGetListAndConcurrentRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(100 * time.Millisecond)
	vms := []string{"vm1", "vm2"}

	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		_, _ = reg.Start(ctx, Connection{}, vms, 1, fake, nil)
	}()

	// Wait until the wave has started and is running
	time.Sleep(20 * time.Millisecond)

	waves := reg.List()
	if len(waves) != 1 {
		t.Fatalf("List mid-run returned %d waves, want 1", len(waves))
	}
	waveID := waves[0].ID

	midWave, ok := reg.Get(waveID)
	if !ok {
		t.Fatalf("Get(%q) mid-run = false, want true", waveID)
	}
	if midWave.Items[0].State != WaveItemRunning && midWave.Items[0].State != WaveItemPending {
		t.Errorf("mid-run state = %q, want running or pending", midWave.Items[0].State)
	}

	// Calling Retry while still running must fail
	_, err := reg.Retry(ctx, waveID, fake, nil)
	if err == nil || err.Error() != fmt.Sprintf("wave %q is already running", waveID) {
		t.Errorf("Retry while running = %v, want 'already running' error", err)
	}

	<-doneCh
}

// Concurrent Retry invocations on the same wave are safe and mutually exclusive.
func TestWaveConcurrentRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	reg := NewRegistry()
	fake := newFakeWaveConverter(50 * time.Millisecond)
	fake.fail["vm1"] = errors.New("initial failure")

	w, err := reg.Start(ctx, Connection{}, []string{"vm1"}, 1, fake, nil)
	if err == nil {
		t.Fatal("expected failure on start")
	}

	// Clear failure for retry
	fake.mu.Lock()
	delete(fake.fail, "vm1")
	fake.mu.Unlock()

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range 3 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = reg.Retry(ctx, w.ID, fake, nil)
		}(i)
	}
	wg.Wait()

	var succeeded int
	var rejected int
	for _, e := range errs {
		if e == nil {
			succeeded++
		} else if e.Error() == fmt.Sprintf("wave %q is already running", w.ID) {
			rejected++
		}
	}
	if succeeded != 1 {
		t.Errorf("succeeded retries = %d, want exactly 1", succeeded)
	}
	if rejected != 2 {
		t.Errorf("rejected retries = %d, want 2", rejected)
	}
}

type fakeLister struct {
	mu    sync.Mutex
	calls int
	inv   Inventory
	err   error
}

func (f *fakeLister) List(ctx context.Context, c Connection) (Inventory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.inv, f.err
}

func (f *fakeLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// WaveConverter resolves VM from inventory, preflights and converts with *Converter.
func TestWaveConverterConvertOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	testVM := VM{
		ID:         "vm-100",
		Name:       "db01",
		Datacenter: "DC0",
		Host:       "DC0_H0",
		PowerState: "off",
		Disks:      []VMDisk{{CapacityBytes: 10 << 30}},
	}

	f := &fake{write: map[string]string{"db01.xml": v2vXML, "db01-disk0": "data"}}
	conv := &Converter{exec: f.exec, look: foundLook}

	pool := t.TempDir()
	wc := &WaveConverter{
		Lister:        &fakeLister{inv: Inventory{VMs: []VM{testVM}}},
		Converter:     conv,
		Opts:          ConvertOpts{PoolPath: pool},
		PoolFreeBytes: 50 << 30,
	}

	var progress []string
	err := wc.ConvertOne(ctx, Connection{URL: "https://vcenter.test", Username: "u", Password: "p"}, "vm-100", func(msg string) {
		progress = append(progress, msg)
	})
	// Adoption fails because of no libvirt client in unit tests (ErrRefused)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("ConvertOne = %v, want adoption ErrRefused", err)
	}

	if len(progress) == 0 || !slices.Contains(progress, "Converting db01 from VMware…") {
		t.Errorf("progress reports missing conversion start: %v", progress)
	}

	// VM not found
	err = wc.ConvertOne(ctx, Connection{}, "missing-vm", nil)
	if err == nil || err.Error() != `VM "missing-vm" not found in VMware inventory` {
		t.Fatalf("expected not found error, got: %v", err)
	}
}

// WaveConverter caches inventory, isolates VM names, and reserves pool space across concurrent conversions.
func TestWaveConverterFeatures(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	vm1 := VM{
		ID:         "vm-1",
		Name:       "app01",
		Datacenter: "DC0",
		Host:       "H0",
		PowerState: "off",
		Disks:      []VMDisk{{CapacityBytes: 20 << 30}}, // needs 20GB + 2GB = 22GB
	}
	vm2 := VM{
		ID:         "vm-2",
		Name:       "app02",
		Datacenter: "DC0",
		Host:       "H0",
		PowerState: "off",
		Disks:      []VMDisk{{CapacityBytes: 20 << 30}}, // needs 20GB + 2GB = 22GB
	}

	f := &fake{
		write: map[string]string{
			"app01.xml":   v2vXML,
			"app01-disk0": "data1",
			"app02.xml":   v2vXML,
			"app02-disk0": "data2",
		},
	}
	delayedExec := func(ctx context.Context, env []string, args []string, onLine func(string)) ([]byte, error) {
		if !slices.Equal(args, []string{"--version"}) {
			select {
			case <-time.After(50 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return f.exec(ctx, env, args, onLine)
	}
	conv := &Converter{exec: delayedExec, look: foundLook}
	lister := &fakeLister{inv: Inventory{VMs: []VM{vm1, vm2}}}

	pool := t.TempDir()
	// Pool has 30 GB free. One VM needs 22GB. Two VMs together need 44GB, so concurrent conversion
	// of both must fail the second VM due to reserved space!
	wc := &WaveConverter{
		Lister:        lister,
		Converter:     conv,
		Opts:          ConvertOpts{PoolPath: pool, Name: "shared-override-must-be-ignored"},
		PoolFreeBytes: 30 << 30,
	}

	reg := NewRegistry()
	w, _ := reg.Start(ctx, Connection{URL: "https://vcenter.test", Username: "u", Password: "p"}, []string{"vm-1", "vm-2"}, 2, wc, nil)

	// 1. Lister.List was called only once (cached)
	if count := lister.callCount(); count != 1 {
		t.Errorf("Lister.List called %d times, want exactly 1 (cached)", count)
	}

	// 2. Space reservation: one failed due to pool space, one reached conversion (and failed on adoption due to no libvirt client)
	var poolSpaceFailed, adoptionFailed int
	for _, it := range w.Items {
		if it.State == WaveItemFailed {
			if strings.Contains(it.Message, "The disks of") && strings.Contains(it.Message, "pool has only 8.0 GiB free") {
				poolSpaceFailed++
			} else if strings.Contains(it.Message, "no libvirt connection") {
				adoptionFailed++
			} else {
				t.Errorf("unexpected failure message: %q", it.Message)
			}
		}
	}

	if poolSpaceFailed != 1 {
		t.Errorf("poolSpaceFailed = %d, want 1", poolSpaceFailed)
	}
	if adoptionFailed != 1 {
		t.Errorf("adoptionFailed = %d, want 1", adoptionFailed)
	}
}
