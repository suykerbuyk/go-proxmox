package proxmox

import (
	"context"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorage_VolumeGetters_NullData pins that ISO, VzTmpl and Import return
// an error, not a panic, when the per-volume read succeeds with
// {"data":null}. They used to dereference the nil result. The error must not
// match ErrNotFound: an empty reply is not evidence that the volume is absent.
func TestStorage_VolumeGetters_NullData(t *testing.T) {
	cases := []struct {
		name string
		path string
		call func(*Storage) (interface{}, error)
	}{
		{"ISO", "^/nodes/node1/storage/local/content/local:iso/null\\.iso$",
			func(s *Storage) (interface{}, error) { return s.ISO(context.Background(), "null.iso") }},
		{"VzTmpl", "^/nodes/node1/storage/local/content/local:vztmpl/null\\.tar\\.zst$",
			func(s *Storage) (interface{}, error) { return s.VzTmpl(context.Background(), "null.tar.zst") }},
		{"Import", "^/nodes/node1/storage/local/content/local:import/null\\.vmx$",
			func(s *Storage) (interface{}, error) { return s.Import(context.Background(), "null.vmx") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer gock.Off()
			gock.New(TestURI).
				Get(tc.path).
				Reply(200).
				JSON(`{"data": null}`)
			storage := &Storage{client: NewClient(TestURI), Node: "node1", Name: "local"}

			var (
				got interface{}
				err error
			)
			require.NotPanics(t, func() { got, err = tc.call(storage) })
			assert.ErrorContains(t, err, "returned no data")
			assert.False(t, IsNotFound(err), "an empty reply must not read as not-found")
			assert.Nil(t, got)
			assert.True(t, gock.IsDone())
		})
	}
}
