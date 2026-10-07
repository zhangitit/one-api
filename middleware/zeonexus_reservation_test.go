package middleware

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnforcedOutputBound(t *testing.T) {
	for _, body := range []string{`{"model":"qwen3-8b","messages":[{"role":"user","content":"中文 true $HOME"}]}`, `{"model":"qwen3-8b","messages":[],"max_tokens":16}`} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
		prompt, out, err := prepareZeoReservation(c)
		if err != nil {
			t.Fatal(err)
		}
		expected := int64(16)
		if !strings.Contains(body, "max_tokens") {
			expected = config.ZeoNexusDefaultReserveTokens
		}
		if out != expected || prompt < int64(len(body)) {
			t.Fatalf("unsafe bound %d / %d", prompt, out)
		}
		normalized, _ := common.GetRequestBody(c)
		var request struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		json.Unmarshal(normalized, &request)
		if request.MaxTokens != expected {
			t.Fatal("upstream has no enforced budget")
		}
	}
}
