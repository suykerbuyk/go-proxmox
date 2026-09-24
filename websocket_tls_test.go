package proxmox

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type customRoundTripper struct{}

func (customRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

// wsClients are ticket-auth clients (no token, so the WebSocket calls get
// past their token refusal) with every kind of transport a client can hold.
func wsClients() map[string]*Client {
	const uri = "https://127.0.0.1:1/api2/json"
	return map[string]*Client{
		"transport":         NewClient(uri, WithInsecureSkipVerify()),
		"retry-wrapped":     NewClient(uri, WithInsecureSkipVerify(), WithRetry()),
		"nil transport":     NewClient(uri, WithHTTPClient(&http.Client{})),
		"custom":            NewClient(uri, WithHTTPClient(&http.Client{Transport: customRoundTripper{}})),
		"retry-wrapped nil": NewClient(uri, WithHTTPClient(&http.Client{}), WithRetry()),
	}
}

// TestWebsocketTLSConfig pins where a WebSocket dial takes its TLS config
// from: the client's *http.Transport, looking through WithRetry's wrapper
// (so WithInsecureSkipVerify still applies with WithRetry), and Go's
// default (nil) with no Transport or a custom RoundTripper.
func TestWebsocketTLSConfig(t *testing.T) {
	clients := wsClients()
	for _, name := range []string{"transport", "retry-wrapped"} {
		tc := clients[name].websocketTLSConfig()
		require.NotNil(t, tc, name)
		assert.True(t, tc.InsecureSkipVerify, name)
	}
	_, wrapped := clients["retry-wrapped"].httpClient.Transport.(*retryRoundTripper)
	require.True(t, wrapped, "WithRetry did not wrap the transport: the retry case tests nothing")
	for _, name := range []string{"nil transport", "custom"} {
		var got *tls.Config
		require.NotPanics(t, func() { got = clients[name].websocketTLSConfig() }, name)
		assert.Nil(t, got, name)
	}
	// WithRetry over no Transport wraps http.DefaultTransport, which is
	// what its requests use, so its TLS config is the one to dial with.
	dt, ok := http.DefaultTransport.(*http.Transport)
	require.True(t, ok)
	var got *tls.Config
	require.NotPanics(t, func() { got = clients["retry-wrapped nil"].websocketTLSConfig() })
	assert.Same(t, dt.TLSClientConfig, got)
}

// TestWebSockets_NoPanicOnAnyTransport: TermWebSocket and VNCWebSocket used
// to assert the Transport unchecked and panicked with no Transport, a custom
// RoundTripper, or WithRetry. Now each dials, and fails to connect.
func TestWebSockets_NoPanicOnAnyTransport(t *testing.T) {
	for name, c := range wsClients() {
		var err error
		require.NotPanics(t, func() { _, _, _, _, err = c.TermWebSocket("/x", &Term{}) }, name)
		assert.Error(t, err, name)
		require.NotPanics(t, func() { _, _, _, _, err = c.VNCWebSocket("/x", &VNC{}) }, name)
		assert.Error(t, err, name)
	}
}

// TestEnsureTransport_ReplacedDefaultTransport: a program may replace
// http.DefaultTransport with its own RoundTripper; ensureTransport then
// returns nil, as it does for any custom RoundTripper, where it panicked.
func TestEnsureTransport_ReplacedDefaultTransport(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })
	http.DefaultTransport = customRoundTripper{}

	c := NewClient("https://127.0.0.1:1/api2/json", WithHTTPClient(&http.Client{}))
	var got interface{}
	require.NotPanics(t, func() { got = c.ensureTransport() })
	assert.Nil(t, got)
	assert.Nil(t, c.httpClient.Transport)
}

// TestWebsocketTLSConfig_DoubleRetryWrap: one *http.Client shared by two
// clients, each WithRetry, is wrapped twice; the TLS config is still the
// base Transport's, so WithInsecureSkipVerify still applies.
func TestWebsocketTLSConfig_DoubleRetryWrap(t *testing.T) {
	const uri = "https://127.0.0.1:1/api2/json"
	shared := &http.Client{}
	_ = NewClient(uri, WithHTTPClient(shared), WithInsecureSkipVerify(), WithRetry())
	c := NewClient(uri, WithHTTPClient(shared), WithRetry())

	outer, ok := shared.Transport.(*retryRoundTripper)
	require.True(t, ok, "not wrapped")
	_, ok = outer.base.(*retryRoundTripper)
	require.True(t, ok, "not wrapped twice: the test tests nothing")

	tc := c.websocketTLSConfig()
	require.NotNil(t, tc)
	assert.True(t, tc.InsecureSkipVerify)
}
