package model

import (
	"encoding/json"
	"testing"
)

func TestActualCacheUsage(t *testing.T) {
	for _, test := range []struct {
		body   string
		cached int
		known  bool
	}{
		{`{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40}}`, 40, true},
		{`{"prompt_tokens":100,"prompt_cache_hit_tokens":0}`, 0, true},
		{`{"prompt_tokens":100,"prompt_cache_hit_tokens":100}`, 100, true},
		{`{"prompt_tokens":100}`, 0, false},
		{`{"prompt_tokens":100,"prompt_tokens_details":{"other":3}}`, 0, false},
		{`{"prompt_tokens":100,"prompt_cache_hit_tokens":101}`, 0, false},
		{`{"prompt_tokens":100,"prompt_cache_hit_tokens":-1}`, 0, false},
		{`{"prompt_tokens":100,"prompt_cache_hit_tokens":1,"prompt_tokens_details":{"cached_tokens":2}}`, 0, false},
	} {
		var usage Usage
		if err := json.Unmarshal([]byte(test.body), &usage); err != nil {
			t.Fatal(err)
		}
		cached, known := usage.CachedInput()
		if cached != test.cached || known != test.known {
			t.Errorf("wrong usage: %s", test.body)
		}
		usage.Estimated = true
		if _, known = usage.CachedInput(); known {
			t.Fatal("estimated usage became billable")
		}
	}
}
