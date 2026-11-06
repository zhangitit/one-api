package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Exercise the public router, credential/model authorization, control plane,
// provider relay and authoritative usage settlement together.
func TestAnthropicMessagesGateway(t *testing.T) {
	restoreAnthropicIntegrationGlobals(t)
	gin.SetMode(gin.TestMode)
	config.ZeoNexusEnabled = true
	config.ZeoNexusProfile = "inference"
	config.ZeoNexusControlSecret = strings.Repeat("c", 40)
	config.ZeoNexusMasterKey = strings.Repeat("m", 40)
	config.ZeoNexusAllowedUpstreamHosts = []string{"127.0.0.1"}
	config.ZeoNexusAllowInsecureHTTP = true
	config.ZeoNexusMaxRequestBodyBytes = 1 << 20
	config.ZeoNexusMaxReserveTokens = 1 << 20
	config.ZeoNexusDefaultReserveTokens = 64
	config.EnforceIncludeUsage = true
	config.ApproximateTokenEnabled = true
	config.MemoryCacheEnabled = false
	config.RetryTimes = 1
	config.AutomaticDisableChannelEnabled = false
	common.RedisEnabled = false
	common.UsingSQLite = true

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "messages.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	model.DB, model.LOG_DB = db, db
	if err = db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	if err = model.MigrateZeoNexus(); err != nil {
		t.Fatal(err)
	}
	client.Init()
	openai.InitTokenEncoders()

	var reserves, finalizes, calls, countCalls, truncatedCalls atomic.Int32
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/billing/gateway/reserve":
			reserves.Add(1)
		case "/billing/gateway/finalize":
			finalizes.Add(1)
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"charged":true,"status":"reserved"}}`)
	}))
	t.Cleanup(console.Close)
	config.ZeoNexusConsoleControlURL = console.URL
	config.ZeoNexusConsoleAllowHTTP = true

	nativeResponse := `{"id":"msg_native","type":"message","role":"assistant","model":"native-upstream","content":[{"type":"thinking","thinking":"checking","signature":"signed-thought"},{"type":"text","text":"OK"},{"type":"tool_use","id":"tool_native","name":"inspect","input":{"path":"a.go"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":3,"cache_creation_input_tokens":2,"cache_read_input_tokens":4,"output_tokens":2},"future_response_field":{"preserved":true}}`
	nativeStream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"native-upstream\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"cache_creation_input_tokens\":2,\"cache_read_input_tokens\":4,\"output_tokens\":1}}}\n\n" +
		"event: ping\ndata: {\"type\":\"ping\"}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"checking\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"signed-thought\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: future_event\ndata: {\"type\":\"future_event\",\"preserved\":true}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("invalid upstream JSON: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/messages/count_tokens" {
			countCalls.Add(1)
			if body["model"] != "native-upstream" || body["max_tokens"] != nil {
				t.Errorf("count_tokens mapping or shape changed: %+v", body)
			}
			if r.Header.Get("X-Api-Key") != "provider-secret" {
				t.Error("count_tokens did not use provider credential")
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"input_tokens":37}`)
			return
		}
		calls.Add(1)
		if config.ZeoNexusEnabled && reserves.Load() < calls.Load() {
			t.Error("provider contacted without cash reservation")
		}
		if body["model"] == "truncated-upstream" {
			truncatedCalls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, strings.Split(nativeStream, "event: message_delta")[0])
			return
		}
		if r.URL.Path == "/v1/messages" {
			if r.Header.Get("X-Api-Key") != "provider-secret" || r.Header.Get("Authorization") == "Bearer "+anthropicIntegrationKey {
				t.Error("native request did not replace gateway credentials")
			}
			if r.Header.Get("Anthropic-Version") != "2023-06-01" || !strings.Contains(strings.Join(r.Header.Values("Anthropic-Beta"), ","), "test-beta-2026-01-01") {
				t.Errorf("native protocol headers lost: %+v", r.Header)
			}
			if r.Header.Get("Anthropic-Future-Capability") != "preserve-me" {
				t.Error("unknown anthropic header was stripped")
			}
			if body["model"] != "native-upstream" || body["thinking"] == nil || body["metadata"] == nil || body["future_request_field"] == nil {
				t.Errorf("native fields or model mapping lost: %+v", body)
			}
			system, ok := body["system"].([]any)
			if !ok || len(system) != 2 || system[0].(map[string]any)["text"] != "first system block" || system[1].(map[string]any)["cache_control"] == nil {
				t.Errorf("native system blocks were changed: %+v", body["system"])
			}
			w.Header().Set("Request-Id", "req_provider")
			w.Header().Set("Anthropic-Ratelimit-Unified-Status", "allowed")
			if body["test_error"] == true {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "17")
				w.Header().Set("X-Should-Retry", "false")
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"type":"error","error":{"type":"api_error","message":"provider-specific error wording"},"request_id":"req_provider"}`)
				return
			}
			if body["stream"] == true {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, event := range strings.SplitAfter(nativeStream, "\n\n") {
					fmt.Fprint(w, event)
					w.(http.Flusher).Flush()
				}
			} else {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, nativeResponse)
			}
			return
		}
		if r.URL.Path != "/v1/chat/completions" || body["model"] != "bridge-upstream" {
			t.Errorf("unexpected bridge URL/model: %s %+v", r.URL.String(), body)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Error("bridge did not use provider credential")
		}
		messages, ok := body["messages"].([]any)
		if !ok {
			t.Errorf("missing converted messages: %+v", body)
		} else {
			var assistantTool, toolResult bool
			for _, item := range messages {
				message := item.(map[string]any)
				assistantTool = assistantTool || (message["role"] == "assistant" && message["tool_calls"] != nil)
				toolResult = toolResult || (message["role"] == "tool" && message["tool_call_id"] == "tool_prior")
			}
			if !assistantTool || !toolResult {
				t.Errorf("tool conversation was not converted: %+v", messages)
			}
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl_bridge\",\"model\":\"bridge-upstream\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_bridge\",\"type\":\"function\",\"function\":{\"name\":\"inspect\",\"arguments\":\"{\\\"path\\\":\"}}]},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl_bridge\",\"model\":\"bridge-upstream\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.go\\\"}\"}}]},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"chatcmpl_bridge\",\"model\":\"bridge-upstream\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11,\"prompt_tokens_details\":{\"cached_tokens\":4}}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl_bridge","object":"chat.completion","model":"bridge-upstream","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_bridge","type":"function","function":{"name":"inspect","arguments":"{\"path\":\"a.go\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":4}}}`)
	}))
	t.Cleanup(upstream.Close)

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetZeoNexusRouter(engine)
	SetRelayRouter(engine)
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/tenants/anthropic-tenant", map[string]any{"name": "Anthropic test", "status": "active", "revision": 1}, http.StatusOK)
	aliases := []string{"native-model", "bridge-model", "truncated-model"}
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/credentials/anthropic-key", map[string]any{
		"tenant_id": "anthropic-tenant", "key_hash": model.HashZeoKey(anthropicIntegrationKey), "key_prefix": "sk-nx-cmp-aaaa", "name": "Messages",
		"profile": "inference", "status": "active", "models": aliases, "expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": 1000000, "revision": 1,
	}, http.StatusOK)
	for i, alias := range aliases {
		typeID, baseURL, actual := 14, upstream.URL, "native-upstream"
		if alias == "bridge-model" {
			typeID, baseURL, actual = 50, upstream.URL+"/v1", "bridge-upstream"
		} else if alias == "truncated-model" {
			actual = "truncated-upstream"
		}
		ref := fmt.Sprintf("anthropic-endpoint-%d", i)
		controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/model-grants/"+ref, map[string]any{
			"tenant_id": "anthropic-tenant", "profile": "inference", "model": alias, "status": "active", "token_limit": 1000000, "revision": 1,
		}, http.StatusOK)
		controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/channels/"+ref, map[string]any{
			"name": ref, "type": typeID, "base_url": baseURL, "key": "provider-secret", "models": []string{alias}, "model_mapping": map[string]string{alias: actual},
			"status": "active", "weight": 1, "priority": 10, "profile": "inference", "site_id": "messages-site", "max_concurrency": 4, "revision": 1,
		}, http.StatusOK)
		controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/routes/"+ref, map[string]any{
			"profile": "inference", "model": alias, "mode": "chat", "channel_ids": ref, "strategy": "priority", "status": "active", "revision": 1,
		}, http.StatusOK)
	}

	// A real fallback makes no-retry assertions meaningful: the selector could
	// otherwise decline a retry simply because only one channel exists.
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/channels/anthropic-fallback", map[string]any{
		"name": "fallback", "type": 14, "base_url": upstream.URL, "key": "provider-secret", "models": []string{"native-model", "truncated-model"},
		"model_mapping": map[string]string{"native-model": "native-upstream", "truncated-model": "truncated-upstream"},
		"status":        "active", "weight": 1, "priority": 0, "profile": "inference", "site_id": "messages-site", "max_concurrency": 4, "revision": 1,
	}, http.StatusOK)
	for _, i := range []int{0, 2} {
		ref := fmt.Sprintf("anthropic-endpoint-%d", i)
		controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/routes/"+ref, map[string]any{
			"profile": "inference", "model": aliases[i], "mode": "chat", "channel_ids": ref + ",anthropic-fallback", "strategy": "priority", "status": "active", "revision": 2,
		}, http.StatusOK)
	}

	nativePayload := map[string]any{
		"model": "native-model", "max_tokens": 32,
		"system":   []map[string]any{{"type": "text", "text": "first system block"}, {"type": "text", "text": "cached system", "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": "hello", "cache_control": map[string]any{"type": "ephemeral"}}}}},
		"tools":    []map[string]any{{"name": "inspect", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "additionalProperties": false}}},
		"thinking": map[string]any{"type": "adaptive"}, "metadata": map[string]any{"user_id": "opaque-test-user"},
		"future_request_field": map[string]any{"preserved": true},
	}
	t.Run("native JSON and cache settlement", func(t *testing.T) {
		response := anthropicIntegrationRequest(t, engine, "/v1/messages?beta=true", anthropicIntegrationKey, false, nativePayload)
		if response.Code != http.StatusOK || response.Body.String() != nativeResponse {
			t.Fatalf("native JSON was not preserved: %d %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Request-Id") != "req_provider" || response.Header().Get("Anthropic-Ratelimit-Unified-Status") != "allowed" {
			t.Fatalf("provider response headers were lost: %+v", response.Header())
		}
		assertAnthropicSettledUsage(t, db, response, false)
	})
	t.Run("native SSE including thinking and future events", func(t *testing.T) {
		nativePayload["stream"] = true
		defer delete(nativePayload, "stream")
		response := anthropicIntegrationRequest(t, engine, "/v1/messages", anthropicIntegrationKey, true, nativePayload)
		if response.Code != http.StatusOK || response.Body.String() != nativeStream {
			t.Fatalf("native SSE changed: %d %s", response.Code, response.Body.String())
		}
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") || !response.Flushed {
			t.Fatal("native SSE was not flushed as an event stream")
		}
		assertAnthropicSettledUsage(t, db, response, true)
	})
	t.Run("token counting does not reserve or charge", func(t *testing.T) {
		beforeReserves, beforeFinalizes := reserves.Load(), finalizes.Load()
		var beforeRows int64
		db.Model(&model.ZeoUsage{}).Count(&beforeRows)
		response := anthropicIntegrationRequest(t, engine, "/v1/messages/count_tokens?beta=true", anthropicIntegrationKey, false, map[string]any{
			"model": "native-model", "system": nativePayload["system"], "messages": nativePayload["messages"], "tools": nativePayload["tools"],
		})
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"input_tokens":37`) {
			t.Fatalf("native count_tokens failed: %d %s", response.Code, response.Body.String())
		}
		var afterRows int64
		db.Model(&model.ZeoUsage{}).Count(&afterRows)
		if countCalls.Load() != 1 || reserves.Load() != beforeReserves || finalizes.Load() != beforeFinalizes || afterRows != beforeRows {
			t.Fatal("count_tokens created usage or cash operations")
		}
	})

	bridgePayload := map[string]any{
		"model": "bridge-model", "max_tokens": 32, "system": "Inspect source code.",
		"tools": []map[string]any{{"name": "inspect", "description": "Inspect a file", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}},
		"messages": []map[string]any{
			{"role": "user", "content": "Inspect this file."},
			{"role": "assistant", "content": []map[string]any{{"type": "tool_use", "id": "tool_prior", "name": "inspect", "input": map[string]any{"path": "prior.go"}}}},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": "tool_prior", "content": "file contents"}, {"type": "text", "text": "Now inspect a.go."}}},
		},
	}
	t.Run("OpenAI bridge tool conversation and JSON", func(t *testing.T) {
		response := anthropicIntegrationRequest(t, engine, "/v1/messages", anthropicIntegrationKey, false, bridgePayload)
		var message struct {
			Type       string `json:"type"`
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content"`
		}
		if json.Unmarshal(response.Body.Bytes(), &message) != nil || response.Code != http.StatusOK || message.Type != "message" || message.StopReason != "tool_use" {
			t.Fatalf("bridge JSON is not an Anthropic message: %d %s", response.Code, response.Body.String())
		}
		if len(message.Content) != 1 || message.Content[0].Type != "tool_use" || message.Content[0].ID != "call_bridge" || message.Content[0].Name != "inspect" || message.Content[0].Input["path"] != "a.go" {
			t.Fatalf("bridge tool arguments lost: %s", response.Body.String())
		}
		assertAnthropicSettledUsage(t, db, response, false)
	})
	t.Run("OpenAI bridge tool JSON deltas and terminal events", func(t *testing.T) {
		bridgePayload["stream"] = true
		defer delete(bridgePayload, "stream")
		response := anthropicIntegrationRequest(t, engine, "/v1/messages", anthropicIntegrationKey, true, bridgePayload)
		body := response.Body.String()
		for _, expected := range []string{"event: message_start", "event: content_block_start", "input_json_delta", "event: content_block_stop", "event: message_delta", `"stop_reason":"tool_use"`, "event: message_stop"} {
			if !strings.Contains(body, expected) {
				t.Fatalf("bridge stream missing %q: %d %s", expected, response.Code, body)
			}
		}
		if response.Code != http.StatusOK || strings.Contains(body, "[DONE]") {
			t.Fatalf("bridge stream retained OpenAI framing: %d %s", response.Code, body)
		}
		assertAnthropicSettledUsage(t, db, response, true)
	})
	t.Run("invalid and overflowing budgets fail before cash or provider access", func(t *testing.T) {
		beforeCalls, beforeReserves := calls.Load()+countCalls.Load(), reserves.Load()
		for _, maximum := range []any{nil, "32", -1, int64(9223372036854775807)} {
			payload := map[string]any{"model": "native-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}}
			if maximum != nil {
				payload["max_tokens"] = maximum
			}
			response := anthropicIntegrationRequest(t, engine, "/v1/messages", anthropicIntegrationKey, false, payload)
			assertAnthropicError(t, response, http.StatusBadRequest)
		}
		if calls.Load()+countCalls.Load() != beforeCalls || reserves.Load() != beforeReserves {
			t.Fatal("invalid budget caused provider access or a cash reservation")
		}
	})
	t.Run("authorization prevents all provider calls", func(t *testing.T) {
		before := calls.Load() + countCalls.Load()
		for _, test := range []struct {
			key, alias string
			status     int
		}{
			{"", "native-model", http.StatusUnauthorized},
			{"sk-nx-agg-" + strings.Repeat("b", 64), "native-model", http.StatusUnauthorized},
			{anthropicIntegrationKey, "ungranted-model", http.StatusForbidden},
		} {
			response := anthropicIntegrationRequest(t, engine, "/v1/messages", test.key, false, map[string]any{"model": test.alias, "max_tokens": 8, "messages": []map[string]string{{"role": "user", "content": "hello"}}})
			assertAnthropicError(t, response, test.status)
		}
		if calls.Load()+countCalls.Load() != before {
			t.Fatal("rejected request reached a provider")
		}
	})
	t.Run("provider errors preserve wording and retry instructions", func(t *testing.T) {
		before := calls.Load()
		nativePayload["test_error"] = true
		defer delete(nativePayload, "test_error")
		response := anthropicIntegrationRequest(t, engine, "/v1/messages", anthropicIntegrationKey, false, nativePayload)
		assertAnthropicError(t, response, http.StatusServiceUnavailable)
		if response.Body.String() != `{"type":"error","error":{"type":"api_error","message":"provider-specific error wording"},"request_id":"req_provider"}` || response.Header().Get("Retry-After") != "17" || response.Header().Get("X-Should-Retry") != "false" {
			t.Fatalf("provider error contract changed: %s %+v", response.Body.String(), response.Header())
		}
		if calls.Load() != before+1 {
			t.Fatal("provider x-should-retry=false was ignored")
		}
	})
	t.Run("truncated SSE remains a reconciliation liability", func(t *testing.T) {
		response := anthropicIntegrationRequest(t, engine, "/v1/messages", anthropicIntegrationKey, false, map[string]any{
			"model": "truncated-model", "max_tokens": 32, "stream": true, "messages": []map[string]string{{"role": "user", "content": "hello"}},
		})
		if truncatedCalls.Load() != 1 || strings.Contains(response.Body.String(), "event: message_stop") || strings.Contains(response.Body.String(), "[DONE]") {
			t.Fatalf("truncated stream was retried or marked complete: %d %s", truncatedCalls.Load(), response.Body.String())
		}
		var usage model.ZeoUsage
		if err := db.Where("request_id = ?", response.Header().Get("X-ZeoNexus-Request-Id")).First(&usage).Error; err != nil {
			t.Fatal(err)
		}
		if usage.Status != model.ZeoUsageReconcile {
			t.Fatalf("partial stream was treated as complete usage: %+v", usage)
		}
	})
	t.Run("legacy x-api-key relays native requests", func(t *testing.T) {
		config.ZeoNexusEnabled = false
		defer func() { config.ZeoNexusEnabled = true }()
		user := model.User{Username: "legacymsgs", Password: "unused-fixture-password", Status: model.UserStatusEnabled, Quota: 9000000000000000, Group: "anthropic-legacy"}
		if err := db.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		token := model.Token{UserId: user.Id, Key: "legacymessagescredential", Status: model.TokenStatusEnabled, ExpiredTime: -1, UnlimitedQuota: true}
		if err := db.Create(&token).Error; err != nil {
			t.Fatal(err)
		}
		base, mapping := upstream.URL, `{"native-model":"native-upstream"}`
		channel := model.Channel{Type: 14, Key: "provider-secret", Status: model.ChannelStatusEnabled, Name: "Legacy Messages", Models: "native-model", Group: user.Group, BaseURL: &base, ModelMapping: &mapping}
		if err := channel.Insert(); err != nil {
			t.Fatal(err)
		}
		legacyEngine := gin.New()
		legacyEngine.Use(middleware.RequestId())
		SetRelayRouter(legacyEngine)
		before := reserves.Load()
		response := anthropicIntegrationRequest(t, legacyEngine, "/v1/messages", "sk-"+token.Key, false, nativePayload)
		if response.Code != http.StatusOK || response.Body.String() != nativeResponse || reserves.Load() != before {
			t.Fatalf("legacy native relay failed: %d %s", response.Code, response.Body.String())
		}
	})
}

const anthropicIntegrationKey = "sk-nx-cmp-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func anthropicIntegrationRequest(t *testing.T, engine http.Handler, path, key string, bearer bool, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	req.Header.Add("Anthropic-Beta", "test-beta-2026-01-01")
	req.Header.Set("Anthropic-Future-Capability", "preserve-me")
	if key != "" {
		if bearer {
			req.Header.Set("Authorization", "Bearer "+key)
		} else {
			req.Header.Set("X-Api-Key", key)
		}
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	return response
}

func assertAnthropicSettledUsage(t *testing.T, db *gorm.DB, response *httptest.ResponseRecorder, stream bool) {
	t.Helper()
	var usage model.ZeoUsage
	if err := db.Where("request_id = ?", response.Header().Get("X-ZeoNexus-Request-Id")).First(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if usage.Status != model.ZeoUsageSuccess || usage.PromptTokens != 9 || usage.CompletionTokens != 2 || usage.TotalTokens != 11 || usage.CachedTokens != 4 || !usage.CacheUsageKnown || usage.IsStream != stream || usage.UsageEstimated {
		t.Fatalf("authoritative usage or cache settlement incorrect: %+v", usage)
	}
}

func assertAnthropicError(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Type != "error" || body.Error.Type == "" || body.Error.Message == "" {
		t.Fatalf("expected Anthropic error %d: %d %s", status, response.Code, response.Body.String())
	}
}

func TestAnthropicRoutesAuthenticateInEveryGatewayMode(t *testing.T) {
	restoreAnthropicIntegrationGlobals(t)
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{"inference", "aggregation", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			config.ZeoNexusEnabled = mode != "legacy"
			config.ZeoNexusProfile = mode
			engine := gin.New()
			SetRelayRouter(engine)
			for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
				response := anthropicIntegrationRequest(t, engine, path, "", false, map[string]any{"model": "test", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 8})
				assertAnthropicError(t, response, http.StatusUnauthorized)
			}
		})
	}
}

// These tests intentionally avoid parallel execution: the gateway configuration
// and its database handles are process globals shared with the existing suite.
func restoreAnthropicIntegrationGlobals(t *testing.T) {
	t.Helper()
	values := []any{
		&config.ZeoNexusEnabled, &config.ZeoNexusProfile, &config.ZeoNexusControlSecret, &config.ZeoNexusMasterKey,
		&config.ZeoNexusAllowedUpstreamHosts, &config.ZeoNexusAllowInsecureHTTP, &config.ZeoNexusMaxRequestBodyBytes,
		&config.ZeoNexusMaxReserveTokens, &config.ZeoNexusDefaultReserveTokens, &config.ZeoNexusConsoleControlURL,
		&config.ZeoNexusConsoleAllowHTTP, &config.EnforceIncludeUsage, &config.ApproximateTokenEnabled,
		&config.MemoryCacheEnabled, &config.RetryTimes, &config.AutomaticDisableChannelEnabled,
		&common.RedisEnabled, &common.UsingSQLite, &model.DB, &model.LOG_DB,
		&client.HTTPClient, &client.ImpatientHTTPClient, &client.UserContentRequestHTTPClient,
	}
	for _, pointer := range values {
		value := reflect.ValueOf(pointer).Elem()
		previous := reflect.New(value.Type()).Elem()
		previous.Set(value)
		t.Cleanup(func() { value.Set(previous) })
	}
	previousMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(previousMode) })
}
