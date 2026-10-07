package router

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
)

func TestZeoNexusGatewayControlAndRelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config.ZeoNexusEnabled = true
	config.ZeoNexusProfile = "inference"
	config.ZeoNexusControlSecret = strings.Repeat("c", 40)
	config.ZeoNexusMasterKey = strings.Repeat("m", 40)
	config.ZeoNexusAllowedUpstreamHosts = []string{"127.0.0.1"}
	config.ZeoNexusAllowInsecureHTTP = true
	config.ZeoNexusMaxRequestBodyBytes = 1024
	config.ZeoNexusMaxReserveTokens = 2048
	config.EnforceIncludeUsage = true
	config.ApproximateTokenEnabled = true
	config.MemoryCacheEnabled = false
	config.RetryTimes = 1
	common.RedisEnabled = false
	common.UsingSQLite = true

	db, err := gorm.Open(sqlite.Open("file:zeonexus-integration?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB, model.LOG_DB = db, db
	if err = db.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}); err != nil {
		t.Fatal(err)
	}
	if err = model.MigrateZeoNexus(); err != nil {
		t.Fatal(err)
	}
	client.Init()
	openai.InitTokenEncoders()

	oldURL, oldHTTP := config.ZeoNexusConsoleControlURL, config.ZeoNexusConsoleAllowHTTP
	defer func() { config.ZeoNexusConsoleControlURL, config.ZeoNexusConsoleAllowHTTP = oldURL, oldHTTP }()
	var moneyReserves, moneyFinalizes atomic.Int32
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		digest := sha256.Sum256(data)
		signed := strings.Join([]string{r.Method, r.URL.RequestURI(), r.Header.Get("X-Zeo-Timestamp"), r.Header.Get("X-Zeo-Nonce"), hex.EncodeToString(digest[:])}, "\n")
		mac := hmac.New(sha256.New, []byte(config.ZeoNexusControlSecret))
		mac.Write([]byte(signed))
		if !hmac.Equal([]byte(r.Header.Get("X-Zeo-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("unsigned money operation")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/billing/gateway/reserve":
			moneyReserves.Add(1)
		case "/billing/gateway/finalize":
			moneyFinalizes.Add(1)
		default:
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"charged":true,"status":"reserved"}}`)
	}))
	defer console.Close()
	config.ZeoNexusConsoleControlURL = console.URL
	config.ZeoNexusConsoleAllowHTTP = true
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			http.Error(w, "bad secret", http.StatusUnauthorized)
			return
		}
		if moneyReserves.Load() <= upstreamCalls.Load() {
			t.Error("upstream started before cash authorization")
		}
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
			fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"model\":\"upstream-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","model":"upstream-model","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}}`)
	}))
	defer upstream.Close()

	engine := gin.New()
	engine.Use(middleware.RequestId())
	SetZeoNexusRouter(engine)
	SetRelayRouter(engine)

	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/tenants/tenant-1", map[string]any{
		"name": "Test tenant", "status": "active", "revision": 1,
	}, http.StatusOK)
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/model-grants/grant-1", map[string]any{
		"tenant_id": "tenant-1", "profile": "inference", "model": "demo-model", "allowed_sites": []string{"site-1"},
		"status": "active", "expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": 100000, "used_tokens": 7, "revision": 1,
	}, http.StatusOK)
	rawKey := "sk-nx-cmp-" + strings.Repeat("a", 64)
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/credentials/key-1", map[string]any{
		"tenant_id": "tenant-1", "key_hash": model.HashZeoKey(rawKey), "key_prefix": "sk-nx-cmp-aaaa", "name": "Integration",
		"profile": "inference", "status": "active", "models": []string{"demo-model"}, "allowed_sites": []string{},
		"expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": 100000, "used_tokens": 7, "revision": 1,
	}, http.StatusOK)
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/channels/endpoint-1", map[string]any{
		"name": "Mock endpoint", "type": 50, "base_url": upstream.URL + "/v1", "key": "upstream-secret", "models": []string{"demo-model"},
		"model_mapping": map[string]string{"demo-model": "upstream-model"}, "status": "active", "weight": 1, "priority": 10,
		"profile": "inference", "site_id": "site-1", "node_id": "node-1", "endpoint_id": "endpoint-1", "max_concurrency": 4, "revision": 1,
	}, http.StatusOK)
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/routes/route-cmp-1", map[string]any{
		"profile": "inference", "model": "demo-model", "mode": "chat", "channel_ids": "endpoint-1", "strategy": "priority", "status": "active", "revision": 1,
	}, http.StatusOK)

	var stored model.Channel
	if err = db.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Key == "upstream-secret" || !strings.HasPrefix(stored.Key, "zeo:v1:") {
		t.Fatalf("channel secret was not encrypted at rest: %q", stored.Key)
	}

	// A periodic configuration snapshot must preserve counters and measurements while a request is active.
	if !model.AcquireZeoChannel(stored.Id) {
		t.Fatal("could not reserve test concurrency")
	}
	if err = db.Model(&model.Channel{}).Where("id = ?", stored.Id).Updates(map[string]any{"used_quota": 17, "response_time": 333, "test_time": 1234}).Error; err != nil {
		t.Fatal(err)
	}
	base, mapping := upstream.URL+"/v1", `{"demo-model":"upstream-model"}`
	managed, err := model.UpsertZeoChannel("endpoint-1", 2, "inference", "site-1", "node-1", "endpoint-1", 4,
		&model.Channel{Type: 50, Name: "Refreshed", Models: "demo-model", Group: "zeonexus-inference", Status: model.ChannelStatusEnabled, BaseURL: &base, ModelMapping: &mapping})
	if err != nil {
		t.Fatal(err)
	}
	var refreshed model.Channel
	var refreshedManaged model.ZeoManagedChannel
	if err = db.First(&refreshed, stored.Id).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.First(&refreshedManaged, managed.Id).Error; err != nil {
		t.Fatal(err)
	}
	if refreshedManaged.ActiveRequests != 1 || refreshed.UsedQuota != 17 || refreshed.ResponseTime != 333 || refreshed.TestTime != 1234 {
		t.Fatal("configuration sync erased live concurrency or telemetry")
	}
	model.ReleaseZeoChannel(stored.Id)

	modelsResponse := publicRequest(t, engine, http.MethodGet, "/v1/models", rawKey, nil)
	if modelsResponse.Code != http.StatusOK || !strings.Contains(modelsResponse.Body.String(), "demo-model") {
		t.Fatalf("model list failed: %d %s", modelsResponse.Code, modelsResponse.Body.String())
	}
	if modelsResponse.Header().Get("X-ZeoNexus-Request-Id") == "" || modelsResponse.Header().Get("X-Oneapi-Request-Id") != "" {
		t.Fatal("ZeoNexus request id header was not applied")
	}
	tooLarge := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": strings.Repeat("x", 2048)}},
	})
	if tooLarge.Code != http.StatusRequestEntityTooLarge || !strings.Contains(tooLarge.Body.String(), "request_too_large") {
		t.Fatalf("oversized request returned %d: %s", tooLarge.Code, tooLarge.Body.String())
	}
	tooManyTokens := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 10000,
	})
	if tooManyTokens.Code != http.StatusBadRequest || !strings.Contains(tooManyTokens.Body.String(), "max_tokens_exceeded") {
		t.Fatalf("excessive max_tokens returned %d: %s", tooManyTokens.Code, tooManyTokens.Body.String())
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("rejected request reached the upstream")
	}

	nonStream := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 16, "stream": false,
	})
	if nonStream.Code != http.StatusOK || !strings.Contains(nonStream.Body.String(), `"content":"OK"`) {
		t.Fatalf("non-stream relay failed: %d %s", nonStream.Code, nonStream.Body.String())
	}
	stream := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 16, "stream": true,
	})
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), "data: [DONE]") {
		t.Fatalf("stream relay failed: %d %s", stream.Code, stream.Body.String())
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", upstreamCalls.Load())
	}

	if moneyReserves.Load() != 2 || moneyFinalizes.Load() != 2 {
		t.Fatal("cash preauthorization and finalization missing")
	}
	wrongKey := publicRequest(t, engine, http.MethodGet, "/v1/models", "sk-nx-agg-"+strings.Repeat("b", 64), nil)
	if wrongKey.Code != http.StatusUnauthorized || !strings.Contains(wrongKey.Body.String(), "invalid_api_key") {
		t.Fatalf("wrong gateway key was not rejected: %d %s", wrongKey.Code, wrongKey.Body.String())
	}
	unsupported := publicRequest(t, engine, http.MethodPost, "/v1/embeddings", rawKey, map[string]any{"model": "demo-model", "input": "hello"})
	if unsupported.Code != http.StatusBadRequest || !strings.Contains(unsupported.Body.String(), "model_mode_mismatch") {
		t.Fatalf("model mode mismatch returned %d: %s", unsupported.Code, unsupported.Body.String())
	}

	usage := controlJSON(t, engine, http.MethodGet, "/internal/nexus/v1/usage?after=0&limit=20", nil, http.StatusOK)
	if !strings.Contains(usage, `"has_more":false`) || strings.Count(usage, `"status":"success"`) != 2 {
		t.Fatalf("usage export failed: %s", usage)
	}
	var credential model.ZeoCredential
	var grant model.ZeoGrant
	if err = db.First(&credential).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.First(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if credential.UsedTokens != 19 || grant.UsedTokens != 19 || credential.ReservedTokens != 0 || grant.ReservedTokens != 0 {
		t.Fatalf("quota settlement mismatch: credential=%+v grant=%+v", credential, grant)
	}
	reconciliation := controlJSON(t, engine, http.MethodGet, "/internal/nexus/v1/usage-reconciliation", nil, http.StatusOK)
	if !strings.Contains(reconciliation, `"external_id":"key-1","used_tokens":19,"reserved_tokens":0`) ||
		!strings.Contains(reconciliation, `"external_id":"grant-1","used_tokens":19,"reserved_tokens":0`) ||
		!strings.Contains(reconciliation, `"max_usage_id":2`) {
		t.Fatalf("usage reconciliation failed: %s", reconciliation)
	}
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/model-grants/grant-1", map[string]any{
		"tenant_id": "tenant-1", "profile": "inference", "model": "demo-model", "allowed_sites": []string{"site-1"},
		"status": "active", "expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": 119, "used_tokens": 0, "revision": 2,
	}, http.StatusOK)
	quotaDenied := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 100, "stream": false,
	})
	if quotaDenied.Code != http.StatusForbidden || !strings.Contains(quotaDenied.Body.String(), "quota_exceeded") {
		t.Fatalf("quota limit was not enforced: %d %s", quotaDenied.Code, quotaDenied.Body.String())
	}
	if upstreamCalls.Load() != 2 {
		t.Fatal("quota-rejected request reached the upstream")
	}
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/credentials/key-1", map[string]any{
		"tenant_id": "tenant-1", "key_hash": model.HashZeoKey(rawKey), "key_prefix": "sk-nx-cmp-aaaa", "name": "Integration",
		"profile": "inference", "status": "active", "models": []string{"demo-model"}, "allowed_sites": []string{},
		"expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": -1, "used_tokens": 0, "revision": 2,
	}, http.StatusOK)
	zeroQuotaDenied := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 8,
	})
	if zeroQuotaDenied.Code != http.StatusForbidden || !strings.Contains(zeroQuotaDenied.Body.String(), "quota_exceeded") {
		t.Fatalf("explicit zero credential quota was not enforced: %d %s", zeroQuotaDenied.Code, zeroQuotaDenied.Body.String())
	}
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/credentials/key-1", map[string]any{
		"tenant_id": "tenant-1", "key_hash": model.HashZeoKey(rawKey), "key_prefix": "sk-nx-cmp-aaaa", "name": "Integration",
		"profile": "inference", "status": "active", "models": []string{"demo-model"}, "allowed_sites": []string{},
		"expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": 100000, "used_tokens": 0, "revision": 3,
	}, http.StatusOK)
	controlJSON(t, engine, http.MethodPut, "/internal/nexus/v1/model-grants/grant-1", map[string]any{
		"tenant_id": "tenant-1", "profile": "inference", "model": "demo-model", "allowed_sites": []string{"site-1"},
		"status": "active", "expires_at": time.Now().Add(time.Hour).Unix(), "token_limit": 100000,
		"rpm": 1, "auto_pause": true, "used_tokens": 0, "revision": 3,
	}, http.StatusOK)
	rateDenied := publicRequest(t, engine, http.MethodPost, "/v1/chat/completions", rawKey, map[string]any{
		"model": "demo-model", "messages": []map[string]string{{"role": "user", "content": "hello"}}, "max_tokens": 8,
	})
	if rateDenied.Code != http.StatusTooManyRequests || !strings.Contains(rateDenied.Body.String(), "rate_limit_exceeded") {
		t.Fatalf("RPM policy was not enforced: %d %s", rateDenied.Code, rateDenied.Body.String())
	}
	if err = db.Where("external_id = ?", "grant-1").First(&grant).Error; err != nil {
		t.Fatal(err)
	}
	if grant.Status != model.ZeoStatusDisabled || grant.Revision != 4 {
		t.Fatalf("automatic pause was not committed: status=%s revision=%d", grant.Status, grant.Revision)
	}
	controlJSON(t, engine, http.MethodDelete, "/internal/nexus/v1/credentials/key-1?revision=4", nil, http.StatusNoContent)
	revoked := publicRequest(t, engine, http.MethodGet, "/v1/models", rawKey, nil)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key returned %d", revoked.Code)
	}

	tampered := signedControlRequest(t, http.MethodGet, "/internal/nexus/v1/usage?after=0&limit=1", nil, "query-nonce-00001")
	tampered.URL.RawQuery = "after=0&limit=500"
	tamperedResponse := httptest.NewRecorder()
	engine.ServeHTTP(tamperedResponse, tampered)
	if tamperedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("signed query tampering returned %d", tamperedResponse.Code)
	}

	path := "/internal/nexus/v1/health"
	req := signedControlRequest(t, http.MethodGet, path, nil, "replay-nonce-0001")
	first := httptest.NewRecorder()
	engine.ServeHTTP(first, req)
	second := httptest.NewRecorder()
	engine.ServeHTTP(second, req.Clone(req.Context()))
	if first.Code != http.StatusOK || second.Code != http.StatusConflict {
		t.Fatalf("HMAC replay check failed: %d/%d", first.Code, second.Code)
	}
}

func publicRequest(t *testing.T, engine http.Handler, method, path, key string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte(nil)
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

var nonceCounter atomic.Uint64

func controlJSON(t *testing.T, engine http.Handler, method, path string, payload any, expected int) string {
	t.Helper()
	body := []byte(nil)
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	nonce := fmt.Sprintf("control-nonce-%08d", nonceCounter.Add(1))
	req := signedControlRequest(t, method, path, body, nonce)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != expected {
		t.Fatalf("%s %s returned %d: %s", method, path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}

func signedControlRequest(t *testing.T, method, path string, body []byte, nonce string) *http.Request {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	hash := sha256.Sum256(body)
	canonical := strings.Join([]string{method, path, timestamp, nonce, hex.EncodeToString(hash[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(config.ZeoNexusControlSecret))
	_, _ = mac.Write([]byte(canonical))
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Zeo-Timestamp", timestamp)
	req.Header.Set("X-Zeo-Nonce", nonce)
	req.Header.Set("X-Zeo-Signature", hex.EncodeToString(mac.Sum(nil)))
	return req
}
