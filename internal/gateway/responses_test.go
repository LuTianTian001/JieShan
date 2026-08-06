package gateway

import "testing"

func TestParseResponsesMeta(t *testing.T) {
	meta, err := parseResponsesMeta([]byte(`{
		"model":"gpt-5.6-mini",
		"stream":true,
		"max_output_tokens":2048,
		"reasoning":{"effort":"high","max_tokens":512}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "gpt-5.6-mini" || !meta.Stream || meta.MaxOutputTokens != 2048 || meta.ReasoningEffort != "high" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
	if meta.ThinkingBudget == nil || *meta.ThinkingBudget != 512 {
		t.Fatalf("unexpected thinking budget: %+v", meta.ThinkingBudget)
	}
}

func TestValidateResponsesResponse(t *testing.T) {
	valid := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message"}]}`)
	if err := validateResponsesResponse(valid); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	invalid := []byte(`{"id":"resp_2","object":"response","status":"failed","error":{"message":"no capacity"}}`)
	if err := validateResponsesResponse(invalid); err == nil {
		t.Fatal("failed response was accepted")
	}
}

func TestParseResponsesUsageEnvelope(t *testing.T) {
	model, parsed := parseUsage([]byte(`{
		"type":"response.completed",
		"response":{
			"model":"gpt-5.6-mini",
			"usage":{
				"input_tokens":100,
				"output_tokens":50,
				"input_tokens_details":{"cached_tokens":40},
				"output_tokens_details":{"reasoning_tokens":20}
			}
		}
	}`), "fallback")
	if model != "gpt-5.6-mini" || !parsed.complete() {
		t.Fatalf("responses usage was not recognized: model=%q usage=%+v", model, parsed)
	}
	if *parsed.Input != 60 || *parsed.CacheRead != 40 || *parsed.Output != 30 || *parsed.Reasoning != 20 {
		t.Fatalf("responses usage was double counted: %+v", parsed)
	}
}
