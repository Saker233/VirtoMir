package virt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSHA512Crypt(t *testing.T) {
	// The test vectors of Drepper's specification (default rounds).
	for _, c := range []struct{ key, salt, want string }{
		{"Hello world!", "saltstring", "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		// rounds=5000 spelled out in the spec; the salt is cut to 16 characters.
		{"This is just a test", "toolongsaltstring", "$6$toolongsaltstrin$lQ8jolhgVRVhY4b5pZKaysCLi0QBxGoNeKQzQ3glMhwllF7oGDZxUhx1yxdYcz/e1JSbq3y6JMxxl8audkUEm0"},
	} {
		if got := sha512Crypt([]byte(c.key), []byte(c.salt)); got != c.want {
			t.Errorf("sha512Crypt(%q, %q) = %s", c.key, c.salt, got)
		}
	}
}

func TestSetRootPassword(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	shadow := filepath.Join(root, "etc", "shadow")
	os.WriteFile(shadow, []byte("root:*:19000:0:99999:7:::\ndaemon:*:19000:0:99999:7:::\n"), 0o640)
	if err := setRootPassword(root, "virtomir-test"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(shadow)
	lines := strings.Split(string(raw), "\n")
	if !strings.HasPrefix(lines[0], "root:$6$") || !strings.HasSuffix(lines[0], ":19000:0:99999:7:::") || lines[1] != "daemon:*:19000:0:99999:7:::" {
		t.Errorf("shadow = %q", raw)
	}
}

func TestConsoleOnlyInittab(t *testing.T) {
	p := filepath.Join(t.TempDir(), "inittab")
	os.WriteFile(p, []byte("::respawn:/sbin/getty 38400 console\ntty1::respawn:/sbin/getty 38400 tty1\n::shutdown:/sbin/openrc shutdown\n"), 0o644)
	if err := consoleOnly(p); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	l := strings.Split(string(raw), "\n")
	if l[0] != "::respawn:/sbin/getty 38400 console" || !strings.HasPrefix(l[1], "#tty1::") || l[2] != "::shutdown:/sbin/openrc shutdown" {
		t.Errorf("inittab = %q", raw)
	}
	if err := consoleOnly(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Error(err)
	}
}
