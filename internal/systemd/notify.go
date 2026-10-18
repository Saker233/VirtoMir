// Package systemd provides zero-dependency Linux systemd sd_notify and watchdog integration.
package systemd

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// State constants for sd_notify.
const (
	Ready     = "READY=1"
	Stopping  = "STOPPING=1"
	Watchdog  = "WATCHDOG=1"
	Reloading = "RELOADING=1"
)

// Notify sends a state notification to the systemd service manager if running under systemd.
// If NOTIFY_SOCKET is not set (e.g. on Windows, dev mode, or non-systemd init), it returns nil.
func Notify(state string) error {
	socketAddr := os.Getenv("NOTIFY_SOCKET")
	if socketAddr == "" {
		return nil
	}

	// Abstract socket addresses start with @ in systemd conventions.
	if strings.HasPrefix(socketAddr, "@") {
		socketAddr = "\x00" + socketAddr[1:]
	}

	conn, err := net.Dial("unixgram", socketAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	_, err = conn.Write([]byte(state))
	return err
}

// WatchdogInterval returns the duration specified in WATCHDOG_USEC, or 0 if disabled.
func WatchdogInterval() time.Duration {
	val := os.Getenv("WATCHDOG_USEC")
	if val == "" {
		return 0
	}
	usec, err := strconv.ParseInt(val, 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	return time.Duration(usec) * time.Microsecond
}

// StartWatchdog starts a background goroutine that periodically pings systemd with WATCHDOG=1
// at half the configured WATCHDOG_USEC interval until ctx is cancelled.
// Returns false if watchdog is not enabled by systemd.
func StartWatchdog(ctx context.Context, logf func(string, ...any)) bool {
	interval := WatchdogInterval()
	if interval <= 0 {
		return false
	}

	// Ping at half the interval to avoid race conditions.
	tickInterval := interval / 2
	if tickInterval < 500*time.Millisecond {
		tickInterval = 500 * time.Millisecond
	}

	if logf != nil {
		logf("systemd watchdog enabled (timeout %v, ping every %v)", interval, tickInterval)
	}

	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = Notify(Watchdog)
			}
		}
	}()

	return true
}
