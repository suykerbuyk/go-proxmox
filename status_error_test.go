package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedResponse is one canned answer from scriptedTransport. Unlike gock,
// it can set the reason phrase in Status, which is where Proxmox puts its
// error message.
type scriptedResponse struct {
	code   int
	status string
	body   string
}

// scriptedTransport answers the Nth request with responses[N], repeating the
// last response once the script runs out.
type scriptedTransport struct {
	mu        sync.Mutex
	responses []scriptedResponse
	calls     int
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.responses[min(s.calls, len(s.responses)-1)]
	s.calls++
	return &http.Response{
		StatusCode: r.code,
		Status:     r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func (s *scriptedTransport) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func scriptedClient(responses ...scriptedResponse) (*Client, *scriptedTransport) {
	rt := &scriptedTransport{responses: responses}
	return NewClient(TestURI, WithHTTPClient(&http.Client{Transport: rt})), rt
}

// requireStatusError asserts err is a *StatusError with the given code.
func requireStatusError(t *testing.T, err error, code int, msgAndArgs ...interface{}) *StatusError {
	t.Helper()
	require.Error(t, err, msgAndArgs...)
	var se *StatusError
	require.True(t, errors.As(err, &se), "%T is not a *StatusError: %v", err, err)
	assert.Equal(t, code, se.StatusCode, msgAndArgs...)
	return se
}

// F1: every non-2xx status that handleResponse has no dedicated branch for
// becomes a *StatusError carrying the exact code, whatever the body, whatever
// the decode target, and whether the call is a read or a write.
func TestClient_NonSuccessStatusIsStatusError(t *testing.T) {
	statuses := []int{304, 404, 405, 409, 502, 503, 504, 595, 596, 599}
	bodies := []string{`{"data":null}`, `{"errors":{"x":"y"}}`, ``, `<html>502</html>`}
	targets := []struct {
		name string
		v    func() interface{}
	}{
		{"struct ptr", func() interface{} { var v *Version; return &v }},
		{"slice ptr", func() interface{} { var v []*Version; return &v }},
		{"nil", func() interface{} { return nil }},
	}

	for _, code := range statuses {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			for _, body := range bodies {
				for _, target := range targets {
					for _, method := range []string{http.MethodGet, http.MethodPost} {
						desc := fmt.Sprintf("%s %d body=%q target=%s", method, code, body, target.name)
						func() {
							defer gock.Off()
							m := gock.New(TestURI)
							if method == http.MethodGet {
								m = m.Get("^/status/probe$")
							} else {
								m = m.Post("^/status/probe$")
							}
							m.Reply(code).BodyString(body)

							c := NewClient(TestURI)
							var err error
							if method == http.MethodGet {
								err = c.Get(context.Background(), "/status/probe", target.v())
							} else {
								err = c.Post(context.Background(), "/status/probe", nil, target.v())
							}

							se := requireStatusError(t, err, code, desc)
							assert.Equal(t, body, string(se.Body), desc)
							assert.Equal(t, code == http.StatusNotFound, IsNotFound(err), "%s: IsNotFound", desc)
							assert.False(t, IsNotAuthorized(err), "%s: IsNotAuthorized", desc)
						}()
					}
				}
			}
		})
	}
}

// F1: the gate keeps the status line, so Error() carries the reason phrase
// Proxmox puts its message in.
func TestClient_NonSuccessStatusKeepsReasonPhrase(t *testing.T) {
	for _, r := range []scriptedResponse{
		{502, "502 Bad Gateway: upstream closed the connection", `{"data":null}`},
		{595, "595 Connection refused", `{"data":null}`},
	} {
		t.Run(fmt.Sprint(r.code), func(t *testing.T) {
			c, _ := scriptedClient(r)
			err := c.Get(context.Background(), "/nodes/node2/status", nil)
			se := requireStatusError(t, err, r.code)
			assert.Equal(t, r.status, se.Status)
			assert.Equal(t, r.status, err.Error())
		})
	}
}

// failingBody returns its content and then a read error.
type failingBody struct{ r io.Reader }

var errBodyRead = errors.New("connection reset")

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, errBodyRead
	}
	return n, err
}

func (b *failingBody) Close() error { return nil }

// F1: a non-2xx whose body cannot be read in full is still a *StatusError,
// carrying what was read and unwrapping to the read error, below 400 as well
// as above it. A 2xx read failure stays the plain read error.
func TestClient_NonSuccessBodyReadFailure(t *testing.T) {
	c := NewClient(TestURI)
	for _, code := range []int{101, 302, 400, 404, 503, 595} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			err := c.handleResponse(&http.Response{
				StatusCode: code,
				Status:     fmt.Sprintf("%d reason", code),
				Body:       &failingBody{r: strings.NewReader(`{"data":`)},
			}, nil)
			se := requireStatusError(t, err, code)
			assert.Equal(t, `{"data":`, string(se.Body))
			assert.Equal(t, fmt.Sprintf("%d reason", code), err.Error())
			assert.ErrorIs(t, err, errBodyRead)
			assert.Equal(t, code == http.StatusNotFound, IsNotFound(err))
		})
	}
	t.Run("200", func(t *testing.T) {
		err := c.handleResponse(&http.Response{
			StatusCode: 200,
			Status:     "200 OK",
			Body:       &failingBody{r: strings.NewReader(`{"data":`)},
		}, &map[string]string{})
		assert.Equal(t, errBodyRead, err)
	})
}

// F1: the error text for a status without a historical message is the status
// line, and falls back to the code and its standard text when there is none.
func TestStatusError_Error(t *testing.T) {
	assert.Equal(t, "595 Connection refused", (&StatusError{StatusCode: 595, Status: "595 Connection refused"}).Error())
	assert.Equal(t, "404 Not Found", (&StatusError{StatusCode: 404}).Error())
	assert.Equal(t, "596", (&StatusError{StatusCode: 596}).Error())
}

// F1: the 2xx bounds are strict at both ends. net/http delivers neither a
// 0 nor a 1xx final status, so this calls handleResponse directly.
func TestClient_handleResponse_StrictSuccessBounds(t *testing.T) {
	c := NewClient(TestURI)
	for _, code := range []int{0, 101, 199, 300} {
		err := c.handleResponse(&http.Response{
			StatusCode: code,
			Body:       io.NopCloser(strings.NewReader(`{"data":null}`)),
		}, nil)
		requireStatusError(t, err, code, "status %d", code)
	}
}

// F2: the 500 and 501 error text stays exactly the status line, reason
// phrase included. WaitForAgent matches on it.
func TestClient_ServerErrorTextIsStatusLine(t *testing.T) {
	cases := []scriptedResponse{
		{500, "500 QEMU guest agent is not running", `{"data":null}`},
		{501, "501 Method 'GET /nodes/node1/nope' not implemented", `{"data":null}`},
	}
	for _, r := range cases {
		t.Run(fmt.Sprint(r.code), func(t *testing.T) {
			for _, v := range []interface{}{nil, &map[string]string{}} {
				c, _ := scriptedClient(r)
				err := c.Get(context.Background(), "/nodes/node1/nope", v)
				requireStatusError(t, err, r.code)
				assert.Equal(t, r.status, err.Error())
				assert.False(t, IsNotFound(err))
			}
		})
	}
}

// F2: WaitForAgent keeps retrying on the 500 "agent not running" text and
// returns once the agent answers.
func TestVirtualMachine_WaitForAgent_RetriesOnAgentNotRunning(t *testing.T) {
	notRunning := scriptedResponse{500, "500 QEMU guest agent is not running", ``}
	c, rt := scriptedClient(notRunning, notRunning,
		scriptedResponse{200, "200 OK", `{"data":{"result":{"id":"debian","name":"Debian GNU/Linux"}}}`})
	vm := &VirtualMachine{client: c, Node: "node1", VMID: 101}

	err := vm.WaitForAgent(context.Background(), 30)

	assert.NoError(t, err)
	assert.Equal(t, 3, rt.callCount())
}

// F3: the 400 error text is unchanged for each body shape, and CheckID still
// classifies Proxmox's "already exists" answer as taken.
func TestClient_BadRequestText(t *testing.T) {
	const status = "400 Parameter verification failed."
	t.Run("errors key", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{400, status, `{"errors":{"vmid":"VM 200 already exists"},"data":null}`})
		err := c.Get(context.Background(), "/probe", nil)
		se := requireStatusError(t, err, 400)
		assert.Equal(t, `bad request: 400 Parameter verification failed. - {"vmid":"VM 200 already exists"}`, err.Error())
		assert.Equal(t, `{"errors":{"vmid":"VM 200 already exists"},"data":null}`, string(se.Body))
	})
	t.Run("no errors key", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{400, status, `{"data":null}`})
		err := c.Get(context.Background(), "/probe", nil)
		requireStatusError(t, err, 400)
		assert.Equal(t, `bad request: 400 Parameter verification failed. - {"data":null}`, err.Error())
	})
	t.Run("non-JSON body", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{400, status, `<html>bad</html>`})
		err := c.Get(context.Background(), "/probe", nil)
		requireStatusError(t, err, 400)
		assert.Equal(t, `invalid character '<' looking for beginning of value`, err.Error())
		var syntaxErr *json.SyntaxError
		assert.True(t, errors.As(err, &syntaxErr), "the decode error stays reachable")
	})
	t.Run("CheckID taken", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{400, status, `{"errors":{"vmid":"VM 200 already exists"},"data":null}`})
		free, err := (&Cluster{client: c}).CheckID(context.Background(), 200)
		assert.NoError(t, err)
		assert.False(t, free)
	})
}

// F4: 2xx handling is unchanged, including 204's decode error when the
// caller asked for a decoded response.
func TestClient_SuccessStatusDecoding(t *testing.T) {
	t.Run("200 with data", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{200, "200 OK", `{"data":{"release":"9.2","version":"9.2.11"}}`})
		var got *Version
		require.NoError(t, c.Get(context.Background(), "/version", &got))
		require.NotNil(t, got)
		assert.Equal(t, "9.2.11", got.Version)
	})
	t.Run("200 null data", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{200, "200 OK", `{"data":null}`})
		var got *Version
		assert.NoError(t, c.Get(context.Background(), "/version", &got))
		assert.Nil(t, got)
	})
	t.Run("201 with data", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{201, "201 Created", `{"data":"UPID:node1:1:1:1:x:1:root@pam:"}`})
		var got UPID
		require.NoError(t, c.Post(context.Background(), "/probe", nil, &got))
		assert.Equal(t, UPID("UPID:node1:1:1:1:x:1:root@pam:"), got)
	})
	t.Run("204 no body, nil target", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{204, "204 No Content", ``})
		assert.NoError(t, c.Delete(context.Background(), "/probe", nil))
	})
	t.Run("204 no body, decode target", func(t *testing.T) {
		c, _ := scriptedClient(scriptedResponse{204, "204 No Content", ``})
		var got *Version
		err := c.Get(context.Background(), "/probe", &got)
		require.Error(t, err)
		assert.Equal(t, "unexpected end of JSON input", err.Error())
		var se *StatusError
		assert.False(t, errors.As(err, &se), "a 2xx decode error is not a StatusError")
	})
}

// F5: UploadReader has no 401/403 branch of its own; an auth failure must
// still come back as an error that IsNotAuthorized recognises.
func TestClient_UploadReader_NotAuthorized(t *testing.T) {
	for _, code := range []int{401, 403} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			defer gock.Off()
			gock.New(TestURI).
				Post("^/nodes/node1/storage/local/upload$").
				Reply(code).
				JSON(`{"data":null}`)

			var upid UPID
			err := NewClient(TestURI).UploadReader("/nodes/node1/storage/local/upload",
				map[string]string{"content": "iso"}, "a.iso", strings.NewReader("x"), 1, &upid)

			requireStatusError(t, err, code)
			assert.True(t, IsNotAuthorized(err))
			assert.False(t, IsNotFound(err))
		})
	}
}

// F6: a swallowed status used to leave these getters dereferencing a nil
// result; each must return an error instead of panicking.
func TestGetters_StatusErrorDoesNotPanic(t *testing.T) {
	getters := []struct {
		name string
		path string
		call func(c *Client) (interface{}, error)
	}{
		{"Node.VirtualMachine", "^/nodes/node1/qemu/101/", func(c *Client) (interface{}, error) {
			return (&Node{client: c, Name: "node1"}).VirtualMachine(context.Background(), 101)
		}},
		{"Node.Container", "^/nodes/node1/lxc/100/", func(c *Client) (interface{}, error) {
			return (&Node{client: c, Name: "node1"}).Container(context.Background(), 100)
		}},
		{"Storage.ISO", "^/nodes/node1/storage/local/content/", func(c *Client) (interface{}, error) {
			return (&Storage{client: c, Node: "node1", Name: "local"}).ISO(context.Background(), "a.iso")
		}},
	}
	for _, g := range getters {
		for _, code := range []int{404, 595} {
			t.Run(fmt.Sprintf("%s %d", g.name, code), func(t *testing.T) {
				defer gock.Off()
				gock.New(TestURI).
					Persist().
					Get(g.path).
					Reply(code).
					JSON(`{"data":null}`)

				var (
					got interface{}
					err error
				)
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("%s panicked on %d: %v", g.name, code, r)
						}
					}()
					got, err = g.call(NewClient(TestURI))
				}()

				requireStatusError(t, err, code)
				assert.Nil(t, got)
				assert.Equal(t, code == http.StatusNotFound, IsNotFound(err))
			})
		}
	}
}

// F7: a task-returning call on an unreachable node returns an error, never a
// nil task with a nil error.
func TestVirtualMachine_Start_StatusError(t *testing.T) {
	defer gock.Off()
	gock.New(TestURI).
		Post("^/nodes/node1/qemu/101/status/start$").
		Reply(595).
		JSON(`{"data":null}`)

	vm := &VirtualMachine{client: NewClient(TestURI), Node: "node1", VMID: 101}
	task, err := vm.Start(context.Background())

	requireStatusError(t, err, 595)
	assert.Nil(t, task)
}

// F8: CheckID must not report a VMID as free when the check itself failed.
func TestCluster_CheckID_StatusError(t *testing.T) {
	defer gock.Off()
	gock.New(TestURI).
		Get("^/cluster/nextid$").
		MatchParam("vmid", "150").
		Reply(595).
		JSON(`{"data":null}`)

	free, err := (&Cluster{client: NewClient(TestURI)}).CheckID(context.Background(), 150)

	requireStatusError(t, err, 595)
	assert.False(t, free)
}

// F9: Task.Wait must not report success when its first status poll failed.
func TestTask_Wait_FirstPollStatusError(t *testing.T) {
	defer gock.Off()
	upid := UPID("UPID:node1:0000A001:0000A001:0000A001:qmstart:101:root@pam:")
	gock.New(TestURI).
		Persist().
		Get("^/nodes/node1/tasks/" + string(upid) + "/status$").
		Reply(595).
		JSON(`{"data":null}`)

	task := NewTask(upid, NewClient(TestURI))
	require.NotNil(t, task)
	err := task.Wait(context.Background(), 10*time.Millisecond, time.Second)

	requireStatusError(t, err, 595)
}
