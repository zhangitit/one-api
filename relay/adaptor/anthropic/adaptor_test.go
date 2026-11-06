package anthropic

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/meta"
)

func TestMessagesRequestURL(t *testing.T) {
	a := &Adaptor{}
	for _, test := range []struct {
		base, path, want string
	}{
		{"https://api.example.com", "/v1/messages", "https://api.example.com/v1/messages"},
		{"https://api.example.com/", "/v1/messages?beta=true", "https://api.example.com/v1/messages?beta=true"},
		{"https://api.example.com/v1/", "/v1/messages", "https://api.example.com/v1/messages"},
		{"https://api.example.com/proxy/v1", "/v1/messages/count_tokens?beta=true", "https://api.example.com/proxy/v1/messages/count_tokens?beta=true"},
	} {
		t.Run(test.want, func(t *testing.T) {
			got, err := a.GetRequestURL(&meta.Meta{BaseURL: test.base, RequestURLPath: test.path})
			if err != nil || got != test.want {
				t.Fatalf("URL = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestMessagesHeadersUseUpstreamCredentialAndPreserveBetas(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("x-api-key", "gateway-secret")
	c.Request.Header.Add("anthropic-beta", "first-beta")
	c.Request.Header.Add("anthropic-beta", "second-beta")
	c.Request.Header.Add("anthropic-future-capability", "preserve-me")
	req := httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
	a := &Adaptor{}
	if err := a.SetupRequestHeader(c, req, &meta.Meta{APIKey: "upstream-secret", ActualModelName: "claude-3-5-sonnet"}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("x-api-key") != "upstream-secret" || req.Header.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("wrong provider credential or version: %v", req.Header)
	}
	if got := req.Header.Values("anthropic-beta"); !reflect.DeepEqual(got, []string{"first-beta", "second-beta"}) {
		t.Fatalf("betas changed: %v", got)
	}
	if req.Header.Get("anthropic-future-capability") != "preserve-me" {
		t.Fatal("future Anthropic capability header was lost")
	}
	c.Request.Header.Del("anthropic-beta")
	c.Request.Header.Set("anthropic-version", "custom-version")
	req = httptest.NewRequest(http.MethodPost, "https://api.example.com/v1/messages", nil)
	if err := a.SetupRequestHeader(c, req, &meta.Meta{APIKey: "upstream-secret"}); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("anthropic-beta") != "" || req.Header.Get("anthropic-version") != "custom-version" {
		t.Fatalf("injected obsolete beta or lost version: %v", req.Header)
	}
}
