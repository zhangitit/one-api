package billing

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"github.com/songquanpeng/one-api/common/config"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSignedReservation(t *testing.T) {
	oldURL, oldHTTP, oldSecret, oldProfile := config.ZeoNexusConsoleControlURL, config.ZeoNexusConsoleAllowHTTP, config.ZeoNexusControlSecret, config.ZeoNexusProfile
	defer func() {
		config.ZeoNexusConsoleControlURL, config.ZeoNexusConsoleAllowHTTP, config.ZeoNexusControlSecret, config.ZeoNexusProfile = oldURL, oldHTTP, oldSecret, oldProfile
	}()
	config.ZeoNexusControlSecret = strings.Repeat("test", 10)
	config.ZeoNexusProfile = "aggregation"
	config.ZeoNexusConsoleAllowHTTP = true
	seen := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hash := sha256.Sum256(body)
		canonical := strings.Join([]string{r.Method, r.URL.RequestURI(), r.Header.Get("X-Zeo-Timestamp"), r.Header.Get("X-Zeo-Nonce"), hex.EncodeToString(hash[:])}, "\n")
		mac := hmac.New(sha256.New, []byte(config.ZeoNexusControlSecret))
		mac.Write([]byte(canonical))
		if r.Header.Get("X-Zeo-Profile") != "aggregation" || !hmac.Equal([]byte(r.Header.Get("X-Zeo-Signature")), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			t.Error("signature/profile mismatch")
		}
		seen++
		w.Header().Set("Content-Type", "application/json")
		if seen == 1 {
			w.Write([]byte(`{"data":{"charged":true,"status":"reserved","amount":"0.00100000"}}`))
		} else {
			w.WriteHeader(403)
			w.Write([]byte(`{"error":{"message":"insufficient funds"}}`))
		}
	}))
	defer server.Close()
	config.ZeoNexusConsoleControlURL = server.URL
	if data, err := Request(context.Background(), "reserve", map[string]any{"request_id": "test-request-0001"}); err != nil || data["status"] != "reserved" {
		t.Fatalf("reservation failed: %v", err)
	}
	if _, err := Request(context.Background(), "reserve", map[string]any{}); err == nil || err.(*Error).Status != 403 {
		t.Fatalf("expected exact billing rejection, got %v", err)
	}
	config.ZeoNexusConsoleAllowHTTP = false
	if _, err := Request(context.Background(), "reserve", map[string]any{}); err == nil || err.(*Error).Status != 503 {
		t.Fatal("unconfigured HTTPS policy must fail closed")
	}
}
func TestRedirectDoesNotReceiveSecret(t *testing.T) {
	oldURL, oldHTTP := config.ZeoNexusConsoleControlURL, config.ZeoNexusConsoleAllowHTTP
	defer func() { config.ZeoNexusConsoleControlURL, config.ZeoNexusConsoleAllowHTTP = oldURL, oldHTTP }()
	followed := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	config.ZeoNexusConsoleControlURL = server.URL
	config.ZeoNexusConsoleAllowHTTP = true
	_, _ = Request(context.Background(), "reserve", map[string]any{})
	if followed {
		t.Fatal("redirect exposed signed headers")
	}
}
