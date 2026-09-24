package proxmox

import (
	"context"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTask_Ping_ExitStatus pins Proxmox's own rule for a stopped task
// (PVE::UPID::status_is_error): "OK" and "WARNINGS: <n>" are successes, and
// anything else is the task's error. A task that completed with warnings
// used to read as failed.
func TestTask_Ping_ExitStatus(t *testing.T) {
	const upid = "UPID:node1:000000E1:000000E1:000000E1:imgdel:local:root@pam:"
	for _, tc := range []struct {
		exit    string
		success bool
	}{
		{"OK", true},
		{"WARNINGS: 1", true},
		{"WARNINGS: 12", true},
		{"WARNINGS: ", false},
		{"WARNINGS:", false},
		{"WARNINGS:3", false},
		{"WARNINGS:  3", false},
		{"warnings: 3", false},
		{"OK WARNINGS", false},
		{"OKAY", false},
		{"WARNINGS: 1x", false},
		{"some WARNINGS: 1", false},
		{"unable to delete volume", false},
		{"unexpected status", false},
	} {
		t.Run(tc.exit, func(t *testing.T) {
			defer gock.Off()
			gock.New(TestURI).
				Get("^/nodes/node1/tasks/" + upid + "/status$").
				Reply(200).
				JSON(`{"data": {"status": "stopped", "exitstatus": "` + tc.exit + `", "node": "node1", "upid": "` + upid + `"}}`)
			task := NewTask(upid, NewClient(TestURI))
			require.NoError(t, task.Ping(context.Background()))
			assert.True(t, task.IsCompleted)
			assert.Equal(t, tc.success, task.IsSuccessful, "IsSuccessful")
			assert.Equal(t, !tc.success, task.IsFailed, "IsFailed")
		})
	}
}
