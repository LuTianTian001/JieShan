package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

type RequestLogFilter struct {
	Limit           int
	BeforeStartedAt *int64
	BeforeID        string
	From            *int64
	To              *int64
	DownstreamKeyID *int64
	SiteID          *int64
	PublicModel     string
	APISurface      string
	Status          string
	Search          string
}

type RequestLogPage struct {
	Items               []RequestLog
	HasMore             bool
	NextBeforeStartedAt *int64
	NextBeforeID        string
}

type RequestLogDetail struct {
	Request         RequestLog
	RouteCandidates []RequestRouteCandidate
	Attempts        []RequestAttempt
	Ledger          []QuotaLedgerEntry
}

type RequestLogSummary struct {
	Requests             int64
	Succeeded            int64
	Failed               int64
	Cancelled            int64
	Running              int64
	SuccessBasisPoints   int
	TotalChargedNanoUSD  int64
	TotalOfficialNanoUSD int64
	TotalAttempts        int64
	RequestsWithSwitches int64
	AverageDurationMS    *int64
	P50DurationMS        *int64
	P95DurationMS        *int64
	P50FirstOutputMS     *int64
	P95FirstOutputMS     *int64
}

func (s *Store) ListRequestLogs(ctx context.Context, filter RequestLogFilter) (RequestLogPage, error) {
	if err := validateRequestLogFilter(filter, true); err != nil {
		return RequestLogPage{}, err
	}
	where, args := requestLogWhere(filter, true)
	query := `SELECT ` + requestLogColumns + ` FROM request_logs l` + where + ` ORDER BY l.started_at DESC,l.id DESC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return RequestLogPage{}, err
	}
	defer rows.Close()
	items := make([]RequestLog, 0, filter.Limit+1)
	for rows.Next() {
		item, err := scanRequestLog(rows)
		if err != nil {
			return RequestLogPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RequestLogPage{}, err
	}
	page := RequestLogPage{Items: items}
	if len(items) > filter.Limit {
		page.HasMore = true
		page.Items = items[:filter.Limit]
		last := page.Items[len(page.Items)-1]
		startedAt := last.StartedAt
		page.NextBeforeStartedAt = &startedAt
		page.NextBeforeID = last.ID
	}
	if err := s.attachFinalRequestAttempts(ctx, page.Items); err != nil {
		return RequestLogPage{}, err
	}
	return page, nil
}

func (s *Store) attachFinalRequestAttempts(ctx context.Context, items []RequestLog) error {
	if len(items) == 0 {
		return nil
	}
	requestIndexes := make(map[string]int, len(items))
	args := make([]any, 0, len(items))
	for index := range items {
		requestIndexes[items[index].ID] = index
		args = append(args, items[index].ID)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(items)), ",")
	rows, err := s.DB.QueryContext(ctx, requestAttemptSelect+` WHERE request_id IN (`+placeholders+`)
AND attempt_index=COALESCE(
  (SELECT l.final_attempt_index FROM request_logs l WHERE l.id=request_attempts.request_id),
  (SELECT MAX(latest.attempt_index) FROM request_attempts latest WHERE latest.request_id=request_attempts.request_id)
)
ORDER BY request_id,attempt_index,id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		attempt, err := scanRequestAttempt(rows)
		if err != nil {
			return err
		}
		index, ok := requestIndexes[attempt.RequestID]
		if !ok {
			continue
		}
		attemptCopy := attempt
		items[index].FinalAttempt = &attemptCopy
	}
	return rows.Err()
}

func (s *Store) GetRequestLogDetail(ctx context.Context, requestID string) (RequestLogDetail, error) {
	request, err := s.GetRequestLog(ctx, requestID)
	if err != nil {
		return RequestLogDetail{}, err
	}
	attempts, err := s.ListRequestAttempts(ctx, request.ID)
	if err != nil {
		return RequestLogDetail{}, err
	}
	routeCandidates, err := s.ListRequestRouteCandidates(ctx, request.ID)
	if err != nil {
		return RequestLogDetail{}, err
	}
	ledger, err := s.ListQuotaLedger(ctx, request.ID)
	if err != nil {
		return RequestLogDetail{}, err
	}
	return RequestLogDetail{Request: request, RouteCandidates: routeCandidates, Attempts: attempts, Ledger: ledger}, nil
}

func (s *Store) ListRequestRouteCandidates(ctx context.Context, requestID string) ([]RequestRouteCandidate, error) {
	return listRequestRouteCandidates(ctx, s.DB, strings.TrimSpace(requestID))
}

func listRequestRouteCandidates(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, requestID string) ([]RequestRouteCandidate, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT request_id,position,published_model_target_id,
published_model_target_revision,provider_model_target_id,provider_model_target_revision,
site_id,site_name_snapshot,endpoint_id,endpoint_name_snapshot,source_model,wire_protocol,api_surface,
credentials_json,initial_eligibility,initial_reason,disposition,disposition_reason,attempt_count,
first_attempt_index,last_attempt_index
FROM request_route_candidates WHERE request_id=? ORDER BY position`, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]RequestRouteCandidate, 0)
	for rows.Next() {
		var item RequestRouteCandidate
		var credentialsJSON []byte
		var dispositionReason sql.NullString
		var firstAttemptIndex, lastAttemptIndex sql.NullInt64
		if err := rows.Scan(&item.RequestID, &item.Position, &item.PublishedModelTargetID,
			&item.PublishedModelTargetRevision, &item.ProviderModelTargetID,
			&item.ProviderModelTargetRevision, &item.SiteID, &item.SiteName,
			&item.EndpointID, &item.EndpointName, &item.SourceModel, &item.WireProtocol,
			&item.APISurface, &credentialsJSON, &item.InitialEligibility, &item.InitialReason,
			&item.Disposition, &dispositionReason, &item.AttemptCount,
			&firstAttemptIndex, &lastAttemptIndex); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(credentialsJSON, &item.Credentials); err != nil {
			return nil, err
		}
		item.DispositionReason = dispositionReason.String
		item.FirstAttemptIndex = nullIntPointer(firstAttemptIndex)
		item.LastAttemptIndex = nullIntPointer(lastAttemptIndex)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SummarizeRequestLogs(ctx context.Context, filter RequestLogFilter) (RequestLogSummary, error) {
	filter.Limit = 1
	filter.BeforeStartedAt = nil
	filter.BeforeID = ""
	if err := validateRequestLogFilter(filter, false); err != nil {
		return RequestLogSummary{}, err
	}
	where, args := requestLogWhere(filter, false)
	rows, err := s.DB.QueryContext(ctx, `SELECT l.status,l.duration_ms,l.first_token_ms,l.charged_nano_usd,
l.official_cost_nano_usd,(SELECT COUNT(*) FROM request_attempts a WHERE a.request_id=l.id)
FROM request_logs l`+where, args...)
	if err != nil {
		return RequestLogSummary{}, err
	}
	defer rows.Close()
	result := RequestLogSummary{}
	durations := make([]int64, 0)
	firstOutputs := make([]int64, 0)
	var durationTotal int64
	for rows.Next() {
		var status string
		var duration, firstOutput sql.NullInt64
		var charged, official, attempts int64
		if err := rows.Scan(&status, &duration, &firstOutput, &charged, &official, &attempts); err != nil {
			return RequestLogSummary{}, err
		}
		result.Requests++
		switch status {
		case "success":
			result.Succeeded++
		case "failed":
			result.Failed++
		case "cancelled":
			result.Cancelled++
		case "running":
			result.Running++
		}
		result.TotalChargedNanoUSD += charged
		result.TotalOfficialNanoUSD += official
		result.TotalAttempts += attempts
		if attempts > 1 {
			result.RequestsWithSwitches++
		}
		if duration.Valid {
			durations = append(durations, duration.Int64)
			durationTotal += duration.Int64
		}
		if firstOutput.Valid {
			firstOutputs = append(firstOutputs, firstOutput.Int64)
		}
	}
	if err := rows.Err(); err != nil {
		return RequestLogSummary{}, err
	}
	settled := result.Succeeded + result.Failed + result.Cancelled
	if settled > 0 {
		result.SuccessBasisPoints = int((result.Succeeded*10_000 + settled/2) / settled)
	}
	if len(durations) > 0 {
		average := durationTotal / int64(len(durations))
		result.AverageDurationMS = &average
		result.P50DurationMS = percentileMillis(durations, 50)
		result.P95DurationMS = percentileMillis(durations, 95)
	}
	if len(firstOutputs) > 0 {
		result.P50FirstOutputMS = percentileMillis(firstOutputs, 50)
		result.P95FirstOutputMS = percentileMillis(firstOutputs, 95)
	}
	return result, nil
}

func validateRequestLogFilter(filter RequestLogFilter, requireLimit bool) error {
	if requireLimit && (filter.Limit < 1 || filter.Limit > 200) {
		return errors.New("request log limit must be between 1 and 200")
	}
	if (filter.BeforeStartedAt == nil) != (strings.TrimSpace(filter.BeforeID) == "") {
		return errors.New("request log cursor is incomplete")
	}
	if filter.BeforeStartedAt != nil && (*filter.BeforeStartedAt < 0 || len(filter.BeforeID) > 128) {
		return errors.New("request log cursor is invalid")
	}
	if filter.From != nil && *filter.From < 0 || filter.To != nil && *filter.To < 0 {
		return errors.New("request log time range is invalid")
	}
	if filter.From != nil && filter.To != nil && *filter.To < *filter.From {
		return errors.New("request log end time cannot precede start time")
	}
	if filter.DownstreamKeyID != nil && *filter.DownstreamKeyID <= 0 || filter.SiteID != nil && *filter.SiteID <= 0 {
		return errors.New("request log key and site filters must be positive")
	}
	return nil
}

func requestLogWhere(filter RequestLogFilter, includeCursor bool) (string, []any) {
	query := strings.Builder{}
	query.WriteString(` WHERE 1=1`)
	args := make([]any, 0, 16)
	if includeCursor && filter.BeforeStartedAt != nil {
		query.WriteString(` AND (l.started_at<? OR (l.started_at=? AND l.id<?))`)
		args = append(args, *filter.BeforeStartedAt, *filter.BeforeStartedAt, filter.BeforeID)
	}
	if filter.From != nil {
		query.WriteString(` AND l.started_at>=?`)
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		query.WriteString(` AND l.started_at<=?`)
		args = append(args, *filter.To)
	}
	if filter.DownstreamKeyID != nil {
		query.WriteString(` AND l.downstream_key_id=?`)
		args = append(args, *filter.DownstreamKeyID)
	}
	if filter.SiteID != nil {
		query.WriteString(` AND EXISTS (SELECT 1 FROM request_attempts site_attempt WHERE site_attempt.request_id=l.id AND site_attempt.site_id=?)`)
		args = append(args, *filter.SiteID)
	}
	appendExact := func(column, value string) {
		if value = strings.TrimSpace(value); value != "" {
			query.WriteString(` AND ` + column + `=? COLLATE NOCASE`)
			args = append(args, value)
		}
	}
	appendExact("l.public_model", filter.PublicModel)
	appendExact("l.api_surface", filter.APISurface)
	appendExact("l.status", filter.Status)
	if search := strings.TrimSpace(filter.Search); search != "" {
		pattern := "%" + escapeLike(search) + "%"
		query.WriteString(` AND (l.id LIKE ? ESCAPE '\' OR l.downstream_key_name_snapshot LIKE ? ESCAPE '\' OR
l.public_model LIKE ? ESCAPE '\' OR l.price_sku LIKE ? ESCAPE '\' OR l.error_code LIKE ? ESCAPE '\' OR
EXISTS (SELECT 1 FROM request_attempts search_attempt WHERE search_attempt.request_id=l.id AND
(search_attempt.site_name_snapshot LIKE ? ESCAPE '\' OR search_attempt.endpoint_name_snapshot LIKE ? ESCAPE '\' OR
search_attempt.credential_name_snapshot LIKE ? ESCAPE '\' OR search_attempt.source_model LIKE ? ESCAPE '\' OR
search_attempt.error_code LIKE ? ESCAPE '\' OR search_attempt.switch_reason LIKE ? ESCAPE '\')))`)
		for range 11 {
			args = append(args, pattern)
		}
	}
	return query.String(), args
}

func percentileMillis(values []int64, percentile int) *int64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	index := (percentile*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	value := sorted[index-1]
	return &value
}
