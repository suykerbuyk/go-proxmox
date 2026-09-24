package proxmox

import (
	"context"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetters_NullData pins that these getters return an error, not a panic,
// when their read succeeds with {"data":null}. Each used to dereference the
// nil result: Storage.Backup and Node.Storage directly, and
// WaitForAgentExecExit through AgentExecStatus. The error must not match
// ErrNotFound: an empty reply is not evidence of absence.
func TestGetters_NullData(t *testing.T) {
	vm := func() *VirtualMachine { return &VirtualMachine{client: NewClient(TestURI), Node: "node1", VMID: 101} }
	cases := []struct {
		name, path string
		call       func() (interface{}, error)
	}{
		{"Storage.Backup", "^/nodes/node1/storage/local/content/local:backup/null\\.vma\\.zst$", func() (interface{}, error) {
			return (&Storage{client: NewClient(TestURI), Node: "node1", Name: "local"}).Backup(context.Background(), "null.vma.zst")
		}},
		{"Node.Storage", "^/nodes/node1/storage/local/status$", func() (interface{}, error) {
			return (&Node{client: NewClient(TestURI), Name: "node1"}).Storage(context.Background(), "local")
		}},
		{"AgentExecStatus", "^/nodes/node1/qemu/101/agent/exec-status$", func() (interface{}, error) {
			return vm().AgentExecStatus(context.Background(), 7)
		}},
		{"WaitForAgentExecExit", "^/nodes/node1/qemu/101/agent/exec-status$", func() (interface{}, error) {
			return vm().WaitForAgentExecExit(context.Background(), 7, 5)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer gock.Off()
			gock.New(TestURI).
				Get(tc.path).
				Reply(200).
				JSON(`{"data": null}`)

			var (
				got interface{}
				err error
			)
			require.NotPanics(t, func() { got, err = tc.call() })
			assert.ErrorContains(t, err, "no data")
			assert.False(t, IsNotFound(err), "an empty reply must not read as not-found")
			assert.Nil(t, got)
			assert.True(t, gock.IsDone())
		})
	}
}
