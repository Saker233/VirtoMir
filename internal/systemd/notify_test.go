package systemd

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNotify_NoSocket(t *testing.T) {
	os.Unsetenv("NOTIFY_SOCKET")
	if err := Notify(Ready); err != nil {
		t.Fatalf("expected nil when NOTIFY_SOCKET is unset, got: %v", err)
	}
}

func TestWatchdogInterval(t *testing.T) {
	os.Unsetenv("WATCHDOG_USEC")
	if d := WatchdogInterval(); d != 0 {
		t.Fatalf("expected 0, got %v", d)
	}

	os.Setenv("WATCHDOG_USEC", "2000000") // 2 seconds
	defer os.Unsetenv("WATCHDOG_USEC")

	if d := WatchdogInterval(); d != 2*time.Second {
		t.Fatalf("expected 2s, got %v", d)
	}
}

func TestStartWatchdog_Disabled(t *testing.T) {
	os.Unsetenv("WATCHDOG_USEC")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if enabled := StartWatchdog(ctx, nil); enabled {
		t.Fatal("expected watchdog to be disabled")
	}
}
