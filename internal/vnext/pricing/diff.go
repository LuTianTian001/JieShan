package pricing

import (
	"reflect"
	"sort"
)

type DiffSummary struct {
	AddedEntries     int `json:"added_entries"`
	RemovedEntries   int `json:"removed_entries"`
	ChangedEntries   int `json:"changed_entries"`
	UnchangedEntries int `json:"unchanged_entries"`
}

type CatalogDiff struct {
	ActiveVersion    string      `json:"active_version,omitempty"`
	ActiveDigest     string      `json:"active_digest,omitempty"`
	CandidateVersion string      `json:"candidate_version"`
	CandidateDigest  string      `json:"candidate_digest"`
	Summary          DiffSummary `json:"summary"`
	Entries          []EntryDiff `json:"entries"`
}

type EntryDiff struct {
	SKU             string           `json:"sku"`
	Kind            string           `json:"kind"`
	MetadataChanged bool             `json:"metadata_changed,omitempty"`
	Rates           []RateDiff       `json:"rates,omitempty"`
	LongContext     *LongContextDiff `json:"long_context,omitempty"`
}

type LongContextDiff struct {
	Kind                  string     `json:"kind"`
	BeforeThresholdTokens int64      `json:"before_threshold_tokens,omitempty"`
	AfterThresholdTokens  int64      `json:"after_threshold_tokens,omitempty"`
	Rates                 []RateDiff `json:"rates,omitempty"`
}

type RateDiff struct {
	Class  TokenClass `json:"class"`
	Kind   string     `json:"kind"`
	Before *Rate      `json:"before,omitempty"`
	After  *Rate      `json:"after,omitempty"`
}

func DiffCatalogs(active *Catalog, candidate Catalog) CatalogDiff {
	diff := CatalogDiff{CandidateVersion: candidate.Version, CandidateDigest: candidate.Digest}
	before := make(map[string]Entry)
	if active != nil {
		diff.ActiveVersion = active.Version
		diff.ActiveDigest = active.Digest
		for _, entry := range active.Entries {
			before[normalizeSKU(entry.SKU)] = entry
		}
	}
	after := make(map[string]Entry, len(candidate.Entries))
	for _, entry := range candidate.Entries {
		after[normalizeSKU(entry.SKU)] = entry
	}
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		oldEntry, hadOld := before[key]
		newEntry, hasNew := after[key]
		switch {
		case !hadOld:
			diff.Summary.AddedEntries++
			diff.Entries = append(diff.Entries, EntryDiff{
				SKU: newEntry.SKU, Kind: "added", Rates: diffRates(nil, newEntry.Rates),
				LongContext: diffLongContext(nil, newEntry.LongContext),
			})
		case !hasNew:
			diff.Summary.RemovedEntries++
			diff.Entries = append(diff.Entries, EntryDiff{
				SKU: oldEntry.SKU, Kind: "removed", Rates: diffRates(oldEntry.Rates, nil),
				LongContext: diffLongContext(oldEntry.LongContext, nil),
			})
		default:
			metadataChanged := !sameEntryMetadata(oldEntry, newEntry)
			rates := diffRates(oldEntry.Rates, newEntry.Rates)
			longContext := diffLongContext(oldEntry.LongContext, newEntry.LongContext)
			if metadataChanged || len(rates) > 0 || longContext != nil {
				diff.Summary.ChangedEntries++
				diff.Entries = append(diff.Entries, EntryDiff{
					SKU: newEntry.SKU, Kind: "changed", MetadataChanged: metadataChanged,
					Rates: rates, LongContext: longContext,
				})
			} else {
				diff.Summary.UnchangedEntries++
			}
		}
	}
	return diff
}

func sameEntryMetadata(left, right Entry) bool {
	left.Rates, right.Rates = nil, nil
	left.LongContext, right.LongContext = nil, nil
	return reflect.DeepEqual(left, right)
}

func diffLongContext(before, after *LongContextTier) *LongContextDiff {
	switch {
	case before == nil && after == nil:
		return nil
	case before == nil:
		return &LongContextDiff{
			Kind: "added", AfterThresholdTokens: after.ThresholdTokens,
			Rates: diffRates(nil, after.Rates),
		}
	case after == nil:
		return &LongContextDiff{
			Kind: "removed", BeforeThresholdTokens: before.ThresholdTokens,
			Rates: diffRates(before.Rates, nil),
		}
	default:
		rates := diffRates(before.Rates, after.Rates)
		if before.ThresholdTokens == after.ThresholdTokens && len(rates) == 0 {
			return nil
		}
		return &LongContextDiff{
			Kind: "changed", BeforeThresholdTokens: before.ThresholdTokens,
			AfterThresholdTokens: after.ThresholdTokens, Rates: rates,
		}
	}
}

func diffRates(before, after []Rate) []RateDiff {
	oldRates := make(map[TokenClass]Rate, len(before))
	newRates := make(map[TokenClass]Rate, len(after))
	classes := make(map[TokenClass]struct{}, len(before)+len(after))
	for _, rate := range before {
		oldRates[rate.Class] = rate
		classes[rate.Class] = struct{}{}
	}
	for _, rate := range after {
		newRates[rate.Class] = rate
		classes[rate.Class] = struct{}{}
	}
	ordered := make([]TokenClass, 0, len(classes))
	for class := range classes {
		ordered = append(ordered, class)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	result := make([]RateDiff, 0)
	for _, class := range ordered {
		oldRate, hadOld := oldRates[class]
		newRate, hasNew := newRates[class]
		switch {
		case !hadOld:
			copy := newRate
			result = append(result, RateDiff{Class: class, Kind: "added", After: &copy})
		case !hasNew:
			copy := oldRate
			result = append(result, RateDiff{Class: class, Kind: "removed", Before: &copy})
		case !reflect.DeepEqual(oldRate, newRate):
			oldCopy, newCopy := oldRate, newRate
			result = append(result, RateDiff{Class: class, Kind: "changed", Before: &oldCopy, After: &newCopy})
		}
	}
	return result
}
