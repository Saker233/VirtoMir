package catalog

import (
	"context"
	"testing"

	"github.com/onurbaydas/virtomir/internal/virt"
)

type listingDeployer struct {
	Deployer
	vols []virt.Volume
}

func (l listingDeployer) ListVolumes(string) ([]virt.Volume, error) { return l.vols, nil }

type plainDeployer struct{ Deployer }

func TestResolveImagePath(t *testing.T) {
	d := listingDeployer{vols: []virt.Volume{{Name: "cirros.img", Path: "/var/lib/libvirt/images/cirros.img"}}}
	cases := []struct {
		name, ref, want string
		dep             Deployer
		wantErr         bool
	}{
		{"absolute path kept", "/srv/x.img", "/srv/x.img", d, false},
		{"name resolved in pool", "cirros.img", "/var/lib/libvirt/images/cirros.img", d, false},
		{"unknown name refused", "nope.img", "", d, true},
		{"backend without listing keeps ref", "cirros.img", "cirros.img", plainDeployer{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveImagePath(c.dep, c.ref, "images")
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("resolveImagePath(%q) = %q, %v; want %q, err=%v", c.ref, got, err, c.want, c.wantErr)
			}
		})
	}
	_ = context.Background
}
