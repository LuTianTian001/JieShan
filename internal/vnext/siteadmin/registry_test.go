package siteadmin

import (
	"net/http"
	"testing"
)

func TestRegistryRejectsDuplicatesAndUnknownKinds(t *testing.T) {
	registry := NewRegistry()
	adapter, err := NewCiiiAdapter(roundTripDoer(func(*http.Request) (*http.Response, error) { return nil, nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(adapter); err == nil {
		t.Fatal("expected duplicate adapter registration to fail")
	}
	resolved, err := registry.Lookup(" CIII ")
	if err != nil || resolved != adapter {
		t.Fatalf("unexpected lookup result adapter=%v err=%v", resolved, err)
	}
	if _, err := registry.Lookup("unknown"); err == nil {
		t.Fatal("expected unknown adapter lookup to fail")
	}
}

type roundTripDoer func(*http.Request) (*http.Response, error)

func (doer roundTripDoer) Do(request *http.Request) (*http.Response, error) {
	return doer(request)
}
