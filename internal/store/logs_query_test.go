package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

type requestLogTestFixture struct {
	store       *Store
	keyOne      int64
	keyTwo      int64
	upstreamOne int64
	upstreamTwo int64
}

func newRequestLogTestFixture(t *testing.T) requestLogTestFixture {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "request-logs.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	keyOne, err := s.CreateDownstreamKey(ctx, DownstreamKeyWrite{Name: "Key One", Enabled: true}, "js_one", "key-one")
	if err != nil {
		t.Fatal(err)
	}
	keyTwo, err := s.CreateDownstreamKey(ctx, DownstreamKeyWrite{Name: "Key Two", Enabled: true}, "js_two", "key-two")
	if err != nil {
		t.Fatal(err)
	}
	upstreamOne := insertRequestLogUpstream(t, s, "Site One")
	upstreamTwo := insertRequestLogUpstream(t, s, "Site Two")

	insertRequestLog(t, s, RequestLog{
		ID: "z-log", DownstreamKeyID: &keyOne, RequestedModel: "gpt-5", ActualModel: "provider-a",
		Status: "success", Stream: true, FirstTokenMS: requestLogInt64(100), DurationMS: requestLogInt64(500),
		CostMicroUSD: 100, StartedAt: 3_000, FinishedAt: requestLogInt64(3_500),
	}, []int64{upstreamOne})
	insertRequestLog(t, s, RequestLog{
		ID: "m-log", DownstreamKeyID: &keyTwo, RequestedModel: "claude", ActualModel: "provider-b",
		Status: "failed", Stream: false, DurationMS: requestLogInt64(700), CostMicroUSD: 200,
		StartedAt: 2_000, FinishedAt: requestLogInt64(2_700),
	}, []int64{upstreamOne, upstreamTwo})
	insertRequestLog(t, s, RequestLog{
		ID: "a-log", DownstreamKeyID: &keyOne, RequestedModel: "gpt-5", ActualModel: "provider-a",
		Status: "success", Stream: true, FirstTokenMS: requestLogInt64(300), DurationMS: requestLogInt64(600),
		CostMicroUSD: 300, StartedAt: 2_000, FinishedAt: requestLogInt64(2_600),
	}, []int64{upstreamOne, upstreamTwo})
	insertRequestLog(t, s, RequestLog{
		ID: "old-log", DownstreamKeyID: &keyOne, RequestedModel: "gpt-5", ActualModel: "provider-old",
		Status: "success", Stream: false, FirstTokenMS: requestLogInt64(500), DurationMS: requestLogInt64(900),
		CostMicroUSD: 400, StartedAt: 1_000, FinishedAt: requestLogInt64(1_900),
	}, []int64{upstreamOne})
	return requestLogTestFixture{store: s, keyOne: keyOne, keyTwo: keyTwo, upstreamOne: upstreamOne, upstreamTwo: upstreamTwo}
}

func TestListRequestLogsPageUsesStableCompositeCursor(t *testing.T) {
	fixture := newRequestLogTestFixture(t)
	ctx := context.Background()
	first, err := fixture.store.ListRequestLogsPage(ctx, RequestLogFilter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestLogIDs(first.Items); !reflect.DeepEqual(got, []string{"z-log", "m-log"}) {
		t.Fatalf("first page IDs = %v", got)
	}
	if !first.HasMore || first.NextCursor == nil || first.NextCursor.BeforeTime != 2_000 || first.NextCursor.BeforeID != "m-log" {
		t.Fatalf("first page cursor = %+v", first)
	}
	second, err := fixture.store.ListRequestLogsPage(ctx, RequestLogFilter{
		BeforeTime: &first.NextCursor.BeforeTime,
		BeforeID:   first.NextCursor.BeforeID,
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestLogIDs(second.Items); !reflect.DeepEqual(got, []string{"a-log", "old-log"}) {
		t.Fatalf("second page IDs = %v", got)
	}
	if second.HasMore || second.NextCursor != nil {
		t.Fatalf("unexpected final cursor = %+v", second)
	}
}

func TestRequestLogFiltersComposeAcrossStoredFieldsAndAttempts(t *testing.T) {
	fixture := newRequestLogTestFixture(t)
	streamed, notSwitched := true, false
	filter := RequestLogFilter{
		Status: "SUCCESS", Model: "GPT-5", UpstreamID: &fixture.upstreamOne,
		DownstreamKeyID: &fixture.keyOne, Stream: &streamed, Switched: &notSwitched,
	}
	page, err := fixture.store.ListRequestLogsPage(context.Background(), filter, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestLogIDs(page.Items); !reflect.DeepEqual(got, []string{"z-log"}) {
		t.Fatalf("composed filter IDs = %v", got)
	}

	switched := true
	page, err = fixture.store.ListRequestLogsPage(context.Background(), RequestLogFilter{
		Model: "provider-b", UpstreamID: &fixture.upstreamTwo, DownstreamKeyID: &fixture.keyTwo, Switched: &switched,
	}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := requestLogIDs(page.Items); !reflect.DeepEqual(got, []string{"m-log"}) {
		t.Fatalf("attempt-backed filter IDs = %v", got)
	}
}

func TestRequestLogSiteAndLegacyUpstreamFiltersDoNotCollide(t *testing.T) {
	fixture := newRequestLogTestFixture(t)
	ctx := context.Background()
	siteID, err := fixture.store.CreateSite(ctx, SiteWrite{Name: "V3 Site", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.DB.Exec(`INSERT INTO request_logs(
id,routing_generation,requested_model,status,is_stream,cost_micro_usd,started_at)
VALUES ('v3-site-log','v3','gpt-v3','success',0,0,4000)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.store.DB.Exec(`INSERT INTO request_attempts(
request_id,attempt_index,routing_generation,site_id,status,created_at)
VALUES ('v3-site-log',0,'v3',?,'success',4000)`, siteID)
	if err != nil {
		t.Fatal(err)
	}

	bySite, err := fixture.store.ListRequestLogsPage(ctx, RequestLogFilter{SiteID: &siteID}, 20)
	if err != nil || !reflect.DeepEqual(requestLogIDs(bySite.Items), []string{"v3-site-log"}) {
		t.Fatalf("site filter = %+v, %v", requestLogIDs(bySite.Items), err)
	}
	byLegacyUpstream, err := fixture.store.ListRequestLogsPage(ctx, RequestLogFilter{UpstreamID: &siteID}, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range byLegacyUpstream.Items {
		if item.ID == "v3-site-log" {
			t.Fatal("legacy upstream filter matched a V3 site with the same numeric ID")
		}
	}
}

func TestSummarizeRequestLogsUsesTheSameFiltersAndNearestRankTTFT(t *testing.T) {
	fixture := newRequestLogTestFixture(t)
	summary, err := fixture.store.SummarizeRequestLogs(context.Background(), RequestLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 4 || summary.SuccessRate != 75 || summary.CostMicroUSD != 1_000 || summary.SwitchRate != 50 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.P50TTFTMS == nil || *summary.P50TTFTMS != 300 || summary.P95TTFTMS == nil || *summary.P95TTFTMS != 500 {
		t.Fatalf("TTFT percentiles = %+v", summary)
	}

	streamed := true
	filter := RequestLogFilter{Status: "success", Model: "gpt-5", Stream: &streamed}
	page, err := fixture.store.ListRequestLogsPage(context.Background(), filter, 20)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := fixture.store.SummarizeRequestLogs(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Count != int64(len(page.Items)) || filtered.Count != 2 || filtered.SuccessRate != 100 || filtered.CostMicroUSD != 400 || filtered.SwitchRate != 50 {
		t.Fatalf("filtered page=%+v summary=%+v", page, filtered)
	}
	if filtered.P50TTFTMS == nil || *filtered.P50TTFTMS != 100 || filtered.P95TTFTMS == nil || *filtered.P95TTFTMS != 300 {
		t.Fatalf("filtered TTFT percentiles = %+v", filtered)
	}

	empty, err := fixture.store.SummarizeRequestLogs(context.Background(), RequestLogFilter{Status: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Count != 0 || empty.SuccessRate != 0 || empty.SwitchRate != 0 || empty.P50TTFTMS != nil || empty.P95TTFTMS != nil {
		t.Fatalf("empty summary = %+v", empty)
	}
}

func TestRequestLogPageRejectsIncompleteCursor(t *testing.T) {
	fixture := newRequestLogTestFixture(t)
	before := int64(2_000)
	if _, err := fixture.store.ListRequestLogsPage(context.Background(), RequestLogFilter{BeforeTime: &before}, 20); err == nil {
		t.Fatal("incomplete cursor error = nil")
	}
}

func TestRequestLogKeepsSelectedResourceNamesAfterSiteDeletion(t *testing.T) {
	fixture := newRequestLogTestFixture(t)
	ctx := context.Background()
	result, err := fixture.store.DB.Exec(`INSERT INTO sites(name,enabled,revision,created_at,updated_at) VALUES ('Snapshot Site',1,1,1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	siteID, _ := result.LastInsertId()
	result, err = fixture.store.DB.Exec(`INSERT INTO inference_endpoints(site_id,name,base_url,wire_protocol,compatibility_profile,auth_scheme,custom_headers_json,position,enabled,revision,created_at,updated_at)
VALUES (?,'Snapshot Endpoint','https://example.com','openai_chat','generic','bearer','{}',0,1,1,1,1)`, siteID)
	if err != nil {
		t.Fatal(err)
	}
	endpointID, _ := result.LastInsertId()
	result, err = fixture.store.DB.Exec(`INSERT INTO inference_credentials(site_id,name,secret_cipher,position,enabled,runtime_state,revision,created_at,updated_at)
VALUES (?,'Snapshot Key',x'01',0,1,'active',1,1,1)`, siteID)
	if err != nil {
		t.Fatal(err)
	}
	credentialID, _ := result.LastInsertId()
	_, err = fixture.store.DB.Exec(`INSERT INTO request_logs(id,routing_generation,requested_model,status,is_stream,cost_micro_usd,started_at)
VALUES ('snapshot-log','v3','gpt-snapshot','success',0,0,5000)`)
	if err != nil {
		t.Fatal(err)
	}
	latency := int64(125)
	if err := fixture.store.AddRequestAttempt(ctx, RequestAttempt{
		RequestID: "snapshot-log", AttemptIndex: 0, RoutingGeneration: "v3", SiteID: &siteID, SiteName: "Snapshot Site",
		EndpointID: &endpointID, EndpointName: "Snapshot Endpoint", InferenceCredentialID: &credentialID, CredentialName: "Snapshot Key",
		Status: "success", LatencyMS: &latency, CreatedAt: 5_000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.DB.Exec(`DELETE FROM sites WHERE id=?`, siteID); err != nil {
		t.Fatal(err)
	}
	item, attempts, err := fixture.store.GetRequestLog(ctx, "snapshot-log")
	if err != nil {
		t.Fatal(err)
	}
	if item.ActualSiteName != "Snapshot Site" || item.ActualEndpointName != "Snapshot Endpoint" || item.ActualCredentialName != "Snapshot Key" {
		t.Fatalf("selected resource snapshots = %+v", item)
	}
	if len(attempts) != 1 || attempts[0].SiteName != "Snapshot Site" || attempts[0].EndpointName != "Snapshot Endpoint" || attempts[0].CredentialName != "Snapshot Key" {
		t.Fatalf("attempt resource snapshots = %+v", attempts)
	}
}

func insertRequestLogUpstream(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	result, err := s.DB.Exec(`INSERT INTO upstreams(name,kind,enabled,custom_headers_json,created_at,updated_at)
VALUES (?,'compatible',1,'{}',1,1)`, name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRequestLog(t *testing.T, s *Store, item RequestLog, upstreamIDs []int64) {
	t.Helper()
	_, err := s.DB.Exec(`INSERT INTO request_logs(
id,downstream_key_id,requested_model,actual_model,status,http_status,is_stream,first_token_ms,duration_ms,cost_micro_usd,started_at,finished_at)
VALUES (?,?,?,?,?,200,?,?,?,?,?,?)`, item.ID, item.DownstreamKeyID, item.RequestedModel, item.ActualModel,
		item.Status, boolInt(item.Stream), item.FirstTokenMS, item.DurationMS, item.CostMicroUSD, item.StartedAt, item.FinishedAt)
	if err != nil {
		t.Fatal(err)
	}
	for index, upstreamID := range upstreamIDs {
		_, err := s.DB.Exec(`INSERT INTO request_attempts(request_id,attempt_index,upstream_id,status,created_at)
VALUES (?,?,?,?,?)`, item.ID, index, upstreamID, "success", item.StartedAt+int64(index))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func requestLogIDs(items []RequestLog) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func requestLogInt64(value int64) *int64 {
	return &value
}
