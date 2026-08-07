package pricing

import "testing"

func TestDiffCatalogsReportsLongContextTierChangesSeparately(t *testing.T) {
	baseEntry := Entry{
		SKU: "model-a", Provider: "provider-a", ModelPattern: "model-a",
		Rates: []Rate{
			{Class: TokenInput, NanoUSDPerMillion: 1},
			{Class: TokenOutput, NanoUSDPerMillion: 2},
		},
	}
	active := Catalog{Version: "prices-1", Digest: "digest-1", Entries: []Entry{baseEntry}}
	candidate := Catalog{Version: "prices-2", Digest: "digest-2", Entries: []Entry{baseEntry}}
	candidate.Entries[0].LongContext = &LongContextTier{ThresholdTokens: 272_000, Rates: []Rate{
		{Class: TokenInput, NanoUSDPerMillion: 3},
		{Class: TokenOutput, NanoUSDPerMillion: 4},
	}}

	diff := DiffCatalogs(&active, candidate)
	if diff.Summary.ChangedEntries != 1 || len(diff.Entries) != 1 {
		t.Fatalf("added tier diff = %+v", diff)
	}
	entry := diff.Entries[0]
	if entry.MetadataChanged || len(entry.Rates) != 0 || entry.LongContext == nil ||
		entry.LongContext.Kind != "added" || entry.LongContext.AfterThresholdTokens != 272_000 ||
		len(entry.LongContext.Rates) != 2 {
		t.Fatalf("added long-context tier = %+v", entry)
	}

	changed := candidate
	changed.Entries = cloneCatalog(candidate).Entries
	changed.Entries[0].LongContext.ThresholdTokens = 300_000
	changed.Entries[0].LongContext.ThresholdInclusive = true
	changed.Entries[0].LongContext.Rates[0].NanoUSDPerMillion = 5
	diff = DiffCatalogs(&candidate, changed)
	if diff.Summary.ChangedEntries != 1 || len(diff.Entries) != 1 || diff.Entries[0].LongContext == nil {
		t.Fatalf("changed tier diff = %+v", diff)
	}
	longContext := diff.Entries[0].LongContext
	if longContext.Kind != "changed" || longContext.BeforeThresholdTokens != 272_000 ||
		longContext.AfterThresholdTokens != 300_000 || longContext.BeforeThresholdInclusive ||
		!longContext.AfterThresholdInclusive || len(longContext.Rates) != 1 ||
		longContext.Rates[0].Class != TokenInput || longContext.Rates[0].Kind != "changed" {
		t.Fatalf("changed long-context details = %+v", longContext)
	}

	inclusiveOnly := candidate
	inclusiveOnly.Entries = cloneCatalog(candidate).Entries
	inclusiveOnly.Entries[0].LongContext.ThresholdInclusive = true
	diff = DiffCatalogs(&candidate, inclusiveOnly)
	if diff.Summary.ChangedEntries != 1 || len(diff.Entries) != 1 ||
		diff.Entries[0].LongContext == nil || !diff.Entries[0].LongContext.AfterThresholdInclusive ||
		len(diff.Entries[0].LongContext.Rates) != 0 {
		t.Fatalf("inclusive-only long-context diff = %+v", diff)
	}
}
