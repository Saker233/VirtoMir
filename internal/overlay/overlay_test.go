package overlay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// fakeHost is the kernel's view: vxlan devices and their flood entries.
type fakeHost struct {
	links   map[string]*link
	fdb     map[string][]string // device -> peer addresses
	changes []string            // mutating commands, in order
}

func newHost() *fakeHost {
	h := &fakeHost{links: map[string]*link{}, fdb: map[string][]string{}}
	h.links["vxlan-other"] = &link{Name: "vxlan-other"} // somebody else's
	return h
}

func (h *fakeHost) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	if strings.HasSuffix(name, "bridge") {
		if args[0] == "-j" { // -j fdb show dev X
			var out []fdb
			for _, p := range h.fdb[args[4]] {
				out = append(out, fdb{MAC: "00:00:00:00:00:00", Dst: p})
			}
			out = append(out, fdb{MAC: "52:54:00:12:34:56", Dst: "192.168.77.99"}) // learned
			return json.Marshal(out)
		}
		h.changes = append(h.changes, "bridge "+cmd)
		dev, dst := args[4], args[6]
		switch args[1] {
		case "append":
			h.fdb[dev] = append(h.fdb[dev], dst)
		case "del":
			h.fdb[dev] = slices.DeleteFunc(h.fdb[dev], func(p string) bool { return p == dst })
		}
		return nil, nil
	}
	if args[0] == "-j" {
		var out []link
		for _, l := range h.links {
			out = append(out, *l)
		}
		return json.Marshal(out)
	}
	h.changes = append(h.changes, "ip "+cmd)
	switch {
	case strings.HasPrefix(cmd, "link add "):
		l := &link{Name: args[2]}
		fmt.Sscan(args[6], &l.LinkInfo.Data.ID)
		fmt.Sscan(args[8], &l.LinkInfo.Data.Port)
		h.links[args[2]] = l
	case strings.HasPrefix(cmd, "link del "):
		delete(h.links, args[2])
		delete(h.fdb, args[2])
	case strings.Contains(cmd, " master "):
		h.links[args[2]].Master = args[4]
	case strings.HasSuffix(cmd, " up"):
		h.links[args[2]].Flags = []string{"BROADCAST", "UP"}
	}
	return nil, nil
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, a := range s {
		out = append(out, netip.MustParseAddr(a))
	}
	return out
}

func TestReconcileCreatesUpdatesAndRemovesTunnels(t *testing.T) {
	h := newHost()
	r := New(h, nil)
	ctx := context.Background()
	want := []Tunnel{{VNI: 100, Bridge: "vxbr100", Peers: addrs("192.168.77.11", "192.168.77.12")}}

	if err := r.Reconcile(ctx, want); err != nil {
		t.Fatal(err)
	}
	l := h.links["vmvx100"]
	if l == nil || l.Master != "vxbr100" || l.LinkInfo.Data.ID != 100 || l.LinkInfo.Data.Port != Port || !slices.Contains(l.Flags, "UP") {
		t.Fatalf("device = %+v", l)
	}
	if got := h.fdb["vmvx100"]; !slices.Equal(got, []string{"192.168.77.11", "192.168.77.12"}) {
		t.Errorf("peers = %v", got)
	}

	// Nothing to do the second time.
	h.changes = nil
	if err := r.Reconcile(ctx, want); err != nil || len(h.changes) != 0 {
		t.Errorf("second pass changed %v (err %v)", h.changes, err)
	}

	// A peer leaves, another joins; the learned entry stays.
	want[0].Peers = addrs("192.168.77.12", "192.168.77.13")
	h.changes = nil
	if err := r.Reconcile(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got := h.fdb["vmvx100"]; !slices.Equal(got, []string{"192.168.77.12", "192.168.77.13"}) {
		t.Errorf("peers after change = %v (%v)", got, h.changes)
	}

	// The network stops: its tunnel goes, other devices stay.
	if err := r.Reconcile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.links["vmvx100"]; ok {
		t.Error("tunnel of a stopped network kept")
	}
	if _, ok := h.links["vxlan-other"]; !ok {
		t.Error("removed a vxlan device VirtoMir does not own")
	}
}

func TestReconcileReplacesADeviceWithTheWrongVNI(t *testing.T) {
	h := newHost()
	h.links["vmvx7"] = &link{Name: "vmvx7", Master: "vxbr7"}
	h.links["vmvx7"].LinkInfo.Data.ID, h.links["vmvx7"].LinkInfo.Data.Port = 8, Port
	if err := New(h, nil).Reconcile(context.Background(), []Tunnel{{VNI: 7, Bridge: "vxbr7"}}); err != nil {
		t.Fatal(err)
	}
	if h.links["vmvx7"].LinkInfo.Data.ID != 7 || h.changes[0] != "ip link del vmvx7" {
		t.Errorf("changes = %v", h.changes)
	}
}
