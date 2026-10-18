package virt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// shiftIDs moves every file's owner and group by base, keeping setuid/setgid bits (chown
// clears them).
func shiftIDs(root string, base int) error {
	return filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		if int(st.Uid) >= base { // already shifted
			return nil
		}
		if err := os.Lchown(p, int(st.Uid)+base, int(st.Gid)+base); err != nil {
			return err
		}
		if m := info.Mode(); m&os.ModeSymlink == 0 && m&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return os.Chmod(p, m)
		}
		return nil
	})
}

// containerPower asks a running container's init to power off or reboot with the signal
// that init understands: libvirt sends SIGTERM, which busybox init (Alpine) takes as reboot
// and systemd ignores. The domain ID of a container is its libvirt_lxc controller, whose
// child is the container's init.
func containerPower(ctl int32, reboot bool) error {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", ctl, ctl))
	if err != nil {
		return err
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return errors.New("the container has no init process")
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return err
	}
	exe, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	var sig syscall.Signal
	switch {
	case strings.Contains(exe, "systemd"):
		sig = syscall.Signal(38) // SIGRTMIN+4 (glibc SIGRTMIN is 34): poweroff; SIGINT reboots
		if reboot {
			sig = syscall.SIGINT
		}
	case strings.HasSuffix(exe, "busybox"):
		sig = syscall.SIGUSR2 // busybox init: SIGUSR2 poweroff, SIGTERM reboot
		if reboot {
			sig = syscall.SIGTERM
		}
	default:
		return errUnknownInit
	}
	return syscall.Kill(pid, sig)
}
