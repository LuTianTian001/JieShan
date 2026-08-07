package platformdetect

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LuTianTian001/JieShan/internal/vnext/siteadmin"
)

func TestDetectorReturnsTrustedMatchedShape(t *testing.T) {
	detector := newTestDetector(t, probeDoer(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/status":
			return probeResponse(http.StatusNotFound, `{}`), nil
		case "/api/v1/settings/public":
			return probeResponse(http.StatusOK, `{"code":0,"message":"success","data":{"version":"1.2.3","site_name":"Relay","server_timezone":"UTC","server_utc_offset":"+00:00","table_page_size_options":[20,50]}}`), nil
		default:
			t.Fatalf("unexpected probe path %q", request.URL.Path)
			return nil, nil
		}
	}))

	result := detector.Detect(t.Context(), Input{SiteID: 7, Origin: "https://relay.example"})
	if result.State != "detected" || result.Verdict != "trusted" || result.SelectedPlatform != "sub2api" ||
		result.Confidence != "high" || result.Score != 90 || !result.Capabilities.SessionRefresh {
		t.Fatalf("matched result = %#v", result)
	}
	if len(result.Candidates) != 1 || len(result.Evidence) != 2 || result.DetectedAt == nil {
		t.Fatalf("matched result details = %#v", result)
	}
}

func TestDetectorReturnsPossibleForAmbiguousSharedStatusShape(t *testing.T) {
	detector := newTestDetector(t, probeDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/status" {
			return probeResponse(http.StatusOK, `{"success":true,"data":{"version":"v0.0.0","system_name":"Custom Relay"}}`), nil
		}
		return probeResponse(http.StatusNotFound, `{}`), nil
	}))

	result := detector.Detect(t.Context(), Input{SiteID: 9, Origin: "https://relay.example"})
	if result.State != "ambiguous" || result.Verdict != "possible" || result.Confidence != "low" || result.Score != 40 {
		t.Fatalf("ambiguous result = %#v", result)
	}
	if len(result.Candidates) != 2 || result.Candidates[0].Platform != "new_api" || result.Candidates[1].Platform != "one_api" {
		t.Fatalf("ambiguous candidates = %#v", result.Candidates)
	}
}

func TestDetectorReturnsUnknownWithoutMatchingEvidence(t *testing.T) {
	detector := newTestDetector(t, probeDoer(func(*http.Request) (*http.Response, error) {
		return probeResponse(http.StatusOK, `{"ok":true}`), nil
	}))

	result := detector.Detect(t.Context(), Input{SiteID: 11, Origin: "https://relay.example"})
	if result.State != "unknown" || result.Verdict != "unknown" || result.Confidence != "unknown" ||
		result.SelectedPlatform != "" || result.DetectedAt != nil || len(result.Candidates) != 0 {
		t.Fatalf("unknown result = %#v", result)
	}
}

func TestDetectorReturnsExplicitRedactedProbeErrors(t *testing.T) {
	const secret = "credential-that-must-not-leak"
	detector := newTestDetector(t, probeDoer(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed with " + secret)
	}))

	result := detector.Detect(t.Context(), Input{SiteID: 13, Origin: "https://relay.example"})
	if result.State != "unknown" || len(result.Errors) != 2 {
		t.Fatalf("error result = %#v", result)
	}
	for _, probeErr := range result.Errors {
		if probeErr.Code != "request_failed" || strings.Contains(probeErr.Message, secret) {
			t.Fatalf("unredacted probe error = %#v", probeErr)
		}
	}
}

func TestDetectorKeepsLockedManualSelectionAuthoritative(t *testing.T) {
	detector := newTestDetector(t, probeDoer(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/v1/settings/public" {
			return probeResponse(http.StatusOK, `{"code":0,"data":{"version":"1","site_name":"Sub2API","server_timezone":"UTC","server_utc_offset":"+00:00","table_page_size_options":[20]}}`), nil
		}
		return probeResponse(http.StatusNotFound, `{}`), nil
	}))

	result := detector.Detect(t.Context(), Input{
		SiteID: 15, Origin: "https://dashboard.example",
		Manual: &ManualSelection{Platform: "one_api", Origin: "https://management.example", Locked: true},
	})
	if result.State != "manual" || result.Verdict != "trusted" || !result.SelectionLocked ||
		result.SelectedPlatform != "one_api" || result.Score != 100 {
		t.Fatalf("manual result = %#v", result)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].Platform != "one_api" || result.Candidates[0].Score != 100 {
		t.Fatalf("manual candidate ordering = %#v", result.Candidates)
	}
}

func newTestDetector(t *testing.T, doer HTTPDoer) *Detector {
	t.Helper()
	lookup := testLookup{}
	for _, adapter := range []siteadmin.Adapter{
		testAdapter{kind: "new_api", capabilities: siteadmin.Capabilities{Balance: true, Usage: true}},
		testAdapter{kind: "one_api", capabilities: siteadmin.Capabilities{Balance: true, Usage: true}},
		testAdapter{kind: "sub2api", capabilities: siteadmin.Capabilities{SessionRefresh: true, Balance: true, Usage: true}},
	} {
		lookup[adapter.Kind()] = adapter
	}
	detector, err := New(doer, lookup)
	if err != nil {
		t.Fatal(err)
	}
	detector.now = func() time.Time { return time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC) }
	return detector
}

type testAdapter struct {
	kind         string
	capabilities siteadmin.Capabilities
}

func (adapter testAdapter) Kind() string                         { return adapter.kind }
func (adapter testAdapter) Capabilities() siteadmin.Capabilities { return adapter.capabilities }

type testLookup map[string]siteadmin.Adapter

func (lookup testLookup) Lookup(kind string) (siteadmin.Adapter, error) {
	adapter, ok := lookup[strings.ToLower(strings.TrimSpace(kind))]
	if !ok {
		return nil, errors.New("not registered")
	}
	return adapter, nil
}

type probeDoer func(*http.Request) (*http.Response, error)

func (doer probeDoer) Do(request *http.Request) (*http.Response, error) { return doer(request) }

func probeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
