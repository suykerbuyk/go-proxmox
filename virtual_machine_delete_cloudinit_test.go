package proxmox

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
)

// requestLog records every request gock intercepts, matched or not, so a
// test can assert that a request was never sent.
type requestLog struct {
	mu   sync.Mutex
	seen []string
}

func observeRequests(t *testing.T) *requestLog {
	t.Helper()
	l := &requestLog{}
	gock.Observe(func(req *http.Request, _ gock.Mock) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.seen = append(l.seen, req.Method+" "+req.URL.Path)
	})
	t.Cleanup(func() { gock.Observe(nil) })
	return l
}

func (l *requestLog) sent(methodAndPath string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.seen {
		if s == methodAndPath {
			return true
		}
	}
	return false
}

const (
	ciNode      = "cinode2"
	ciVMID      = 101
	ciVMPath    = "/nodes/cinode2/qemu/101"
	ciISOVolume = "user-data-101.iso"
)

func ciContentPath(storage string) string {
	return "/nodes/" + ciNode + "/storage/" + storage + "/content"
}

func ciISOPath(storage string) string {
	return ciContentPath(storage) + "/" + storage + ":iso/" + ciISOVolume
}

// mockCloudInitNode registers the node status and a storage list holding
// the given iso-capable, enabled storages.
func mockCloudInitNode(storages ...string) {
	list := ""
	for i, s := range storages {
		if i > 0 {
			list += ","
		}
		list += `{"storage": "` + s + `", "type": "dir", "enabled": 1, "active": 1, "content": "iso,vztmpl"}`
	}
	mockCloudInitNodeRaw(list)
}

func mockCloudInitNodeRaw(storageList string) {
	gock.New(TestURI).
		Get("^/nodes/" + ciNode + "/status$").
		Reply(200).
		JSON(`{"data": {"name": "` + ciNode + `", "status": "online"}}`)
	gock.New(TestURI).
		Get("^/nodes/" + ciNode + "/storage$").
		Reply(200).
		JSON(`{"data": [` + storageList + `]}`)
}

// mockListing serves a storage's content listing.
func mockListing(storage string, code int, body string) {
	gock.New(TestURI).
		Get("^" + ciContentPath(storage) + "$").
		Reply(code).
		BodyString(body)
}

const (
	listingWithoutISO = `{"data": [{"volid": "st1:iso/debian-12.iso", "format": "iso"}]}`
	listingEmpty      = `{"data": []}`
	listingNull       = `{"data": null}`
)

func listingWithISO(storage string) string {
	return `{"data": [{"volid": "` + storage + `:iso/` + ciISOVolume + `", "format": "iso"}]}`
}

func ciTaskUPID(storage string) string {
	return "UPID:" + ciNode + ":0000D101:0000D101:0000D101:imgdel:" + storage + ":root@pam:"
}

// mockISOLookup serves the ISO's per-volume GET as found.
func mockISOLookup(storage string) {
	gock.New(TestURI).
		Get("^" + ciISOPath(storage) + "$").
		Reply(200).
		JSON(`{"data": {"volid": "` + storage + `:iso/` + ciISOVolume + `", "format": "iso", "size": 374784}}`)
}

// mockISOLookupStatus serves the ISO's per-volume GET with a JSON-bodied
// error status. Proxmox answers a volume that does not exist with a 500.
func mockISOLookupStatus(storage string, code int) {
	gock.New(TestURI).
		Persist().
		Get("^" + ciISOPath(storage) + "$").
		Reply(code).
		JSON(`{"data": null}`)
}

// mockISODeleteOnly registers the ISO DELETE and its task finishing OK.
func mockISODeleteOnly(storage string) {
	gock.New(TestURI).
		Delete("^" + ciISOPath(storage) + "$").
		Reply(200).
		JSON(`{"data": "` + ciTaskUPID(storage) + `"}`)
	gock.New(TestURI).
		Persist().
		Get("^/nodes/" + ciNode + "/tasks/" + ciTaskUPID(storage) + "/status$").
		Reply(200).
		JSON(`{"data": {"status": "stopped", "exitstatus": "OK", "node": "` + ciNode + `", "upid": "` + ciTaskUPID(storage) + `"}}`)
}

// mockISODelete registers the ISO's per-volume GET, its DELETE, and the
// resulting task finishing OK.
func mockISODelete(storage string) {
	mockISOLookup(storage)
	mockISODeleteOnly(storage)
}

func mockVMDelete() {
	gock.New(TestURI).
		Delete("^" + ciVMPath + "$").
		Reply(200).
		JSON(`{"data": "UPID:` + ciNode + `:0000D102:0000D102:0000D102:qmdestroy:101:root@pam:"}`)
}

func cloudInitVM() *VirtualMachine {
	return &VirtualMachine{
		client: NewClient(TestURI),
		Node:   ciNode,
		VMID:   ciVMID,
		VirtualMachineConfig: &VirtualMachineConfig{
			Tags: MakeTag(TagCloudInit),
		},
	}
}

// deleteCloudInitVM runs VirtualMachine.Delete and turns a panic into a
// test failure, so a mutant that panics is reported against its case.
func deleteCloudInitVM(t *testing.T) (*Task, error) {
	t.Helper()
	var (
		task *Task
		err  error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Delete panicked: %v", r)
			}
		}()
		task, err = cloudInitVM().Delete(context.Background(), nil)
	}()
	return task, err
}

// TestVirtualMachine_Delete_CloudInitISOLookup pins how VirtualMachine.Delete
// treats the cloud-init ISO lookup. A lookup that finds the ISO deletes it,
// whatever the listing says. A failed lookup is resolved by the storage's
// content listing, because Proxmox answers a missing volume with a 500: a
// listing that shows the ISO, cannot be read, or carries no data stops the
// delete before the VM DELETE is sent; a listing without the ISO skips the
// storage.
func TestVirtualMachine_Delete_CloudInitISOLookup(t *testing.T) {
	cases := []struct {
		name  string
		setup func()
		// wantErr: Delete returns an error and no VM DELETE is sent.
		wantErr bool
		// wantErrIs, when set, must be reachable from the error with
		// errors.Is: a refusal wraps the errors behind it.
		wantErrIs error
		// isoDeletedOn names the storage whose ISO DELETE must be sent.
		isoDeletedOn string
	}{
		{
			name: "failed lookup and listing 500 stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 500, ``)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "failed lookup and null listing stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingNull)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "listing 404 stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 404, listingNull)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "listing 503 stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 503, listingNull)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "listing 595 stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 595, listingNull)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "listed iso whose lookup answers 503 stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISOLookupStatus("st1", 503)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "listed iso whose lookup answers 595 stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISOLookupStatus("st1", 595)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			// An unmocked request fails with gock.ErrCannotMatch, which gives
			// each refusal a distinct error to check it wraps.
			name: "refusal on a null listing wraps the lookup error",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingNull)
				mockVMDelete()
			},
			wantErr:   true,
			wantErrIs: gock.ErrCannotMatch,
		},
		{
			name: "refusal on a failed listing wraps the lookup error",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 500, ``)
				mockVMDelete()
			},
			wantErr:   true,
			wantErrIs: gock.ErrCannotMatch,
		},
		{
			name: "refusal on a failed listing wraps the listing error",
			setup: func() {
				mockCloudInitNode("st1")
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr:   true,
			wantErrIs: gock.ErrCannotMatch,
		},
		{
			name: "listed iso whose lookup fails stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			// A 200 {"data":null} lookup is an error, not a panic, so the
			// listing decides as it does for a failed lookup.
			name: "null lookup and a listed iso stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISOLookupStatus("st1", 200)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "null lookup and a listing without the iso is skipped",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithoutISO)
				mockISOLookupStatus("st1", 200)
				mockVMDelete()
			},
		},
		{
			name: "iso DELETE failure stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISOLookup("st1")
				gock.New(TestURI).Delete("^" + ciISOPath("st1") + "$").Reply(500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "iso delete task poll failure stops the delete",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISOLookup("st1")
				gock.New(TestURI).
					Delete("^" + ciISOPath("st1") + "$").
					Reply(200).
					JSON(`{"data": "` + ciTaskUPID("st1") + `"}`)
				gock.New(TestURI).
					Persist().
					Get("^/nodes/" + ciNode + "/tasks/" + ciTaskUPID("st1") + "/status$").
					Reply(500)
				mockVMDelete()
			},
			wantErr: true,
		},
		{
			name: "iso absent from the listing and the lookup is skipped",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithoutISO)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
		},
		{
			name: "empty listing and a missing-volume lookup is skipped",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingEmpty)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
		},
		{
			name: "a null entry in the listing is skipped without a panic",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, `{"data": [null]}`)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
		},
		{
			name: "disabled storage is not consulted",
			setup: func() {
				mockCloudInitNodeRaw(
					`{"storage": "st0", "type": "dir", "enabled": 0, "active": 0, "content": "iso"},` +
						`{"storage": "st1", "type": "dir", "enabled": 1, "active": 1, "content": "iso"}`)
				mockListing("st0", 500, ``)
				mockISOLookupStatus("st0", 500)
				mockListing("st1", 200, listingWithoutISO)
				mockISOLookupStatus("st1", 500)
				mockVMDelete()
			},
		},
		{
			name: "iso present is deleted before the vm",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithISO("st1"))
				mockISODelete("st1")
				mockVMDelete()
			},
			isoDeletedOn: "st1",
		},
		{
			name: "iso found by the lookup is deleted even when the listing fails",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 500, ``)
				mockISODelete("st1")
				mockVMDelete()
			},
			isoDeletedOn: "st1",
		},
		{
			name: "iso found by the lookup is deleted even when the listing is null",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingNull)
				mockISODelete("st1")
				mockVMDelete()
			},
			isoDeletedOn: "st1",
		},
		{
			name: "iso missing from an empty listing but found by the lookup is deleted",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingEmpty)
				mockISODelete("st1")
				mockVMDelete()
			},
			isoDeletedOn: "st1",
		},
		{
			name: "iso missing from a listing but found by the lookup is deleted",
			setup: func() {
				mockCloudInitNode("st1")
				mockListing("st1", 200, listingWithoutISO)
				mockISODelete("st1")
				mockVMDelete()
			},
			isoDeletedOn: "st1",
		},
		{
			name: "iso on the second storage is found past a missing-volume 500 on the first",
			setup: func() {
				mockCloudInitNode("st1", "st2")
				mockListing("st1", 200, listingEmpty)
				mockISOLookupStatus("st1", 500)
				mockListing("st2", 200, listingWithISO("st2"))
				mockISODelete("st2")
				mockVMDelete()
			},
			isoDeletedOn: "st2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer gock.Off()
			log := observeRequests(t)
			tc.setup()

			task, err := deleteCloudInitVM(t)

			if tc.wantErr {
				assert.Error(t, err)
				if tc.wantErrIs != nil {
					assert.ErrorIs(t, err, tc.wantErrIs)
				}
				assert.Nil(t, task)
				assert.False(t, log.sent(http.MethodDelete+" "+ciVMPath), "VM DELETE sent after a failed ISO lookup")
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, task)
			assert.True(t, log.sent(http.MethodDelete+" "+ciVMPath), "VM DELETE not sent")
			for _, s := range []string{"st1", "st2"} {
				assert.Equal(t, s == tc.isoDeletedOn, log.sent(http.MethodDelete+" "+ciISOPath(s)),
					"ISO DELETE on %s", s)
			}
		})
	}
}

// TestVirtualMachine_Delete_CloudInitISONeverLeaked replays fixtures in
// which the per-volume lookup finds the ISO, and asserts only that the VM is
// never deleted while that ISO is left behind. Before the content listing was
// consulted, each of these fixtures already cleaned up correctly, so no
// listing outcome may make one of them worse. (A listed ISO whose lookup
// fails leaked before; that case is pinned in
// TestVirtualMachine_Delete_CloudInitISOLookup.)
func TestVirtualMachine_Delete_CloudInitISONeverLeaked(t *testing.T) {
	cases := []struct {
		name        string
		listingCode int
		listing     string
		lookup      func()
	}{
		{"null listing, lookup finds it", 200, listingNull, func() { mockISOLookup("st1") }},
		{"empty listing, lookup finds it", 200, listingEmpty, func() { mockISOLookup("st1") }},
		{"listing without it, lookup finds it", 200, listingWithoutISO, func() { mockISOLookup("st1") }},
		{"listing 500, lookup finds it", 500, ``, func() { mockISOLookup("st1") }},
		{"listing 503, lookup finds it", 503, listingNull, func() { mockISOLookup("st1") }},
		{"listing 595, lookup finds it", 595, listingNull, func() { mockISOLookup("st1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer gock.Off()
			log := observeRequests(t)
			mockCloudInitNode("st1")
			mockListing("st1", tc.listingCode, tc.listing)
			tc.lookup()
			mockISODeleteOnly("st1")
			mockVMDelete()

			_, err := deleteCloudInitVM(t)

			vmDeleted := log.sent(http.MethodDelete + " " + ciVMPath)
			isoDeleted := log.sent(http.MethodDelete + " " + ciISOPath("st1"))
			t.Logf("err=%v vmDeleted=%v isoDeleted=%v", err, vmDeleted, isoDeleted)
			assert.False(t, vmDeleted && !isoDeleted, "the VM was deleted and its existing ISO left behind")
		})
	}
}
