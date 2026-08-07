package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestPolicyBlocksMetadataAndPrivateAddresses(t *testing.T) {
	publicPolicy := &policy{}
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/computeMetadata/v1",
		"http://127.0.0.1:4000",
		"http://10.0.0.1",
		"http://192.0.2.1",
	} {
		target, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := publicPolicy.validateURL(target); err == nil {
			t.Fatalf("expected %s to be blocked", raw)
		}
	}
	private := &policy{allowPrivate: true}
	loopback, _ := url.Parse("http://127.0.0.1:4000")
	if err := private.validateURL(loopback); err != nil {
		t.Fatalf("expected explicit private mode to allow loopback: %v", err)
	}
	metadata, _ := url.Parse("http://169.254.169.254/latest")
	if err := private.validateURL(metadata); err == nil {
		t.Fatal("private mode must never allow metadata addresses")
	}
}

func TestDialRejectsDNSRebindingBeforeNetworkDial(t *testing.T) {
	dialed := false
	policy := &policy{
		resolver: fakeResolver{addresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		dialer: dialerFunc(func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("must not dial")
		}),
	}
	if _, err := policy.dialContext(t.Context(), "tcp", "example.com:443"); err == nil {
		t.Fatal("expected private DNS answer to be blocked")
	}
	if dialed {
		t.Fatal("network dial occurred before resolved address validation")
	}
}

func TestCrossOriginRedirectStripsCredentials(t *testing.T) {
	policy := &policy{}
	previous, _ := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	request, _ := http.NewRequest(http.MethodGet, "https://other.example.com/next?key=secret&model=a", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("CF-Access-Client-Secret", "secret")
	request.Header.Set("X-Trace-ID", "trace")
	if err := policy.checkRedirect(request, []*http.Request{previous}); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("CF-Access-Client-Secret") != "" {
		t.Fatal("cross-origin redirect retained secret headers")
	}
	if request.URL.Query().Get("key") != "" || request.URL.Query().Get("model") != "a" {
		t.Fatal("cross-origin redirect did not selectively remove query credentials")
	}
	if request.Header.Get("X-Trace-ID") != "trace" {
		t.Fatal("non-secret header was removed")
	}
}

func TestCrossOriginRedirectWithBodyIsRejected(t *testing.T) {
	policy := &policy{}
	previous, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/chat/completions", nil)
	request, _ := http.NewRequest(http.MethodPost, "https://other.example.com/next", io.NopCloser(strings.NewReader("payload")))
	if err := policy.checkRedirect(request, []*http.Request{previous}); err == nil {
		t.Fatal("expected body redirect to be rejected")
	}
}

func TestNewUsesBoundedReusableTransportForSmallHosts(t *testing.T) {
	client := New(Options{})
	t.Cleanup(client.CloseIdleConnections)
	guarded, ok := client.http.Transport.(guardedTransport)
	if !ok {
		t.Fatalf("transport type = %T, want guardedTransport", client.http.Transport)
	}
	transport, ok := guarded.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport type = %T, want *http.Transport", guarded.base)
	}
	if transport.MaxConnsPerHost != 12 || transport.MaxIdleConnsPerHost != 6 || transport.MaxIdleConns != 24 {
		t.Fatalf("connection pool = max %d idle-host %d idle-total %d, want 12/6/24",
			transport.MaxConnsPerHost, transport.MaxIdleConnsPerHost, transport.MaxIdleConns)
	}
	if transport.MaxResponseHeaderBytes != 1<<20 || transport.ReadBufferSize != 16<<10 ||
		transport.WriteBufferSize != 16<<10 || !transport.ForceAttemptHTTP2 {
		t.Fatalf("transport memory guards are incomplete: %+v", transport)
	}
}

type fakeResolver struct {
	addresses []netip.Addr
	err       error
}

func (resolver fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return resolver.addresses, resolver.err
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (dialer dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return dialer(ctx, network, address)
}
