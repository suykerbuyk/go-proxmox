package proxmox

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
)

// TestGuestFirewallIPSetDelete_ParamsOnWire pins that the guest ipset DELETEs
// send force and digest as query parameters. They used to pass the parameter
// map as Delete's response target, so neither was ever sent: force=1 was
// ignored, and an unsent digest silently disabled PVE's compare-and-swap.
func TestGuestFirewallIPSetDelete_ParamsOnWire(t *testing.T) {
	vm := &VirtualMachine{client: NewClient(TestURI), Node: "node1", VMID: 101}
	ct := &Container{client: NewClient(TestURI), Node: "node1", VMID: 100}
	const (
		vmSet   = "/nodes/node1/qemu/101/firewall/ipset/blocked"
		ctSet   = "/nodes/node1/lxc/100/firewall/ipset/blocked"
		vmEntry = vmSet + "/10.1.2.3"
		ctEntry = ctSet + "/10.1.2.3"
	)
	cases := []struct {
		name      string
		path      string
		call      func(context.Context) error
		wantQuery string
	}{
		{"vm ipset force", vmSet, func(c context.Context) error { return vm.DeleteFirewallIPSet(c, "blocked", true) }, "force=1"},
		{"vm ipset no force", vmSet, func(c context.Context) error { return vm.DeleteFirewallIPSet(c, "blocked", false) }, ""},
		{"vm entry digest", vmEntry, func(c context.Context) error { return vm.DeleteFirewallIPSetEntry(c, "blocked", "10.1.2.3", "abc") }, "digest=abc"},
		{"vm entry no digest", vmEntry, func(c context.Context) error { return vm.DeleteFirewallIPSetEntry(c, "blocked", "10.1.2.3", "") }, ""},
		{"ct ipset force", ctSet, func(c context.Context) error { return ct.DeleteFirewallIPSet(c, "blocked", true) }, "force=1"},
		{"ct ipset no force", ctSet, func(c context.Context) error { return ct.DeleteFirewallIPSet(c, "blocked", false) }, ""},
		{"ct entry digest", ctEntry, func(c context.Context) error { return ct.DeleteFirewallIPSetEntry(c, "blocked", "10.1.2.3", "abc") }, "digest=abc"},
		{"ct entry no digest", ctEntry, func(c context.Context) error { return ct.DeleteFirewallIPSetEntry(c, "blocked", "10.1.2.3", "") }, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer gock.Off()
			var (
				mu   sync.Mutex
				seen []string
			)
			gock.Observe(func(req *http.Request, _ gock.Mock) {
				mu.Lock()
				defer mu.Unlock()
				seen = append(seen, req.Method+" "+req.URL.String())
			})
			defer gock.Observe(nil)
			gock.New(TestURI).
				Delete("^" + tc.path + "$").
				Reply(200).
				JSON(`{"data": null}`)

			assert.NoError(t, tc.call(context.Background()))

			want := http.MethodDelete + " " + TestURI + tc.path
			if tc.wantQuery != "" {
				want += "?" + tc.wantQuery
			}
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, []string{want}, seen)
		})
	}
}
