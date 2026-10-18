package hostinfo

import (
	"os"
	"path/filepath"
	"strconv"
)

// KSMInfo holds the Linux Kernel Samepage Merging (memory deduplication) status and metrics.
type KSMInfo struct {
	Supported      bool   `json:"supported"`
	Active         bool   `json:"active"`
	PagesShared    uint64 `json:"pagesShared"`
	PagesSharing   uint64 `json:"pagesSharing"`
	PagesUnshared  uint64 `json:"pagesUnshared"`
	PagesVolatile  uint64 `json:"pagesVolatile"`
	SavedBytes     uint64 `json:"savedBytes"`
	SleepMillisecs uint64 `json:"sleepMillisecs"`
	PagesToScan    uint64 `json:"pagesToScan"`
}

// KSM reads the current KSM deduplication state and metrics from /sys/kernel/mm/ksm.
func (r *Reader) KSM() KSMInfo {
	ksmDir := r.path("sys/kernel/mm/ksm")
	if _, err := os.Stat(ksmDir); err != nil {
		return KSMInfo{Supported: false}
	}

	readU64 := func(name string) uint64 {
		val, ok := r.readInt(filepath.Join("sys/kernel/mm/ksm", name))
		if !ok || val < 0 {
			return 0
		}
		return uint64(val)
	}

	runVal := readU64("run")
	pagesSharing := readU64("pages_sharing")
	pagesShared := readU64("pages_shared")
	pagesUnshared := readU64("pages_unshared")
	pagesVolatile := readU64("pages_volatile")
	sleepMs := readU64("sleep_millisecs")
	pagesToScan := readU64("pages_to_scan")

	pageSize := uint64(os.Getpagesize())
	if pageSize == 0 {
		pageSize = 4096
	}

	return KSMInfo{
		Supported:      true,
		Active:         runVal == 1,
		PagesShared:    pagesShared,
		PagesSharing:   pagesSharing,
		PagesUnshared:  pagesUnshared,
		PagesVolatile:  pagesVolatile,
		SavedBytes:     pagesSharing * pageSize,
		SleepMillisecs: sleepMs,
		PagesToScan:    pagesToScan,
	}
}

// SetKSM enables or disables KSM memory deduplication by writing 1 or 0 to /sys/kernel/mm/ksm/run.
func (r *Reader) SetKSM(active bool) error {
	runPath := r.path("sys/kernel/mm/ksm/run")
	val := "0"
	if active {
		val = "1"
	}
	return os.WriteFile(runPath, []byte(val+"\n"), 0644)
}

// SetKSMTune configures scan speed parameters.
func (r *Reader) SetKSMTune(sleepMs, pagesToScan uint64) error {
	if sleepMs > 0 {
		_ = os.WriteFile(r.path("sys/kernel/mm/ksm/sleep_millisecs"), []byte(strconv.FormatUint(sleepMs, 10)+"\n"), 0644)
	}
	if pagesToScan > 0 {
		_ = os.WriteFile(r.path("sys/kernel/mm/ksm/pages_to_scan"), []byte(strconv.FormatUint(pagesToScan, 10)+"\n"), 0644)
	}
	return nil
}
