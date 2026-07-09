package resolver

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestIsSafeRemoteURLRejectsLocalAndPrivateTargets(t *testing.T) {
	blocked := []string{
		"http://localhost/users/alice",
		"http://127.0.0.1/users/alice",
		"http://10.0.0.2/users/alice",
		"http://172.16.0.2/users/alice",
		"http://192.168.1.2/users/alice",
		"http://169.254.1.2/users/alice",
		"http://[::1]/users/alice",
		"file:///tmp/actor.json",
		"ftp://remote.example/users/alice",
	}

	for _, rawURL := range blocked {
		if IsSafeRemoteURL(rawURL) {
			t.Fatalf("%s should be blocked", rawURL)
		}
	}
}

func TestIsSafeRemoteURLAllowsHTTPAndHTTPSPublicHosts(t *testing.T) {
	allowed := []string{
		"https://remote.example/users/alice",
		"http://social.example/users/alice",
	}

	for _, rawURL := range allowed {
		if !IsSafeRemoteURL(rawURL) {
			t.Fatalf("%s should be allowed", rawURL)
		}
	}
}

func TestIsSafeRemoteURLWithLookupRejectsDNSPrivateTargets(t *testing.T) {
	allowed, err := IsSafeRemoteURLWithLookup(context.Background(), "https://evil.example/users/alice", func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	})
	if err != nil {
		t.Fatalf("IsSafeRemoteURLWithLookup returned error: %v", err)
	}
	if allowed {
		t.Fatal("hostname resolving to loopback should be blocked")
	}
}

func TestIsSafeRemoteURLWithLookupAllowsPublicResolvedTargets(t *testing.T) {
	allowed, err := IsSafeRemoteURLWithLookup(context.Background(), "https://remote.example/users/alice", func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	})
	if err != nil {
		t.Fatalf("IsSafeRemoteURLWithLookup returned error: %v", err)
	}
	if !allowed {
		t.Fatal("hostname resolving to a public address should be allowed")
	}
}

func TestHTTPResolverRejectsUnsafeActorInboxURL(t *testing.T) {
	resolver := HTTPResolver{
		client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{
					"id":"https://remote.example/users/bob",
					"type":"Person",
					"preferredUsername":"bob",
					"inbox":"http://127.0.0.1/internal"
				}`)),
			}, nil
		})},
		maxBodyBytes: 1024,
		lookup: func(context.Context, string) ([]net.IP, error) {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		},
	}

	_, err := resolver.ResolveActor(context.Background(), "https://remote.example/users/bob")
	if err == nil {
		t.Fatal("expected unsafe actor inbox URL to be rejected")
	}
	if !strings.Contains(err.Error(), "inbox") {
		t.Fatalf("expected inbox error, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
