package proxmox

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
)

// TestClient_DeleteTFAEntry_PasswordOnWire pins that the password is sent,
// as a query parameter: PVE reads a DELETE's parameters from the query string
// only. It used to be passed as Delete's response target, so it was never
// sent.
func TestClient_DeleteTFAEntry_PasswordOnWire(t *testing.T) {
	for _, tc := range []struct {
		name, password, wantQuery string
	}{
		{"password", "current pw&x", "password=current+pw%26x"},
		{"no password", "", ""},
	} {
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
				Delete("^/access/tfa/alice@pve/totp-1$").
				Reply(200).
				JSON(`{"data": null}`)

			assert.NoError(t, NewClient(TestURI).DeleteTFAEntry(context.Background(), "alice@pve", "totp-1", tc.password))

			want := http.MethodDelete + " " + TestURI + "/access/tfa/alice@pve/totp-1"
			if tc.wantQuery != "" {
				want += "?" + tc.wantQuery
			}
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, []string{want}, seen)
		})
	}
}
