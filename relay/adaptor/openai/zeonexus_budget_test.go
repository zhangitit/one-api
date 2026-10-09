package openai

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
	"net/http/httptest"
	"testing"
)

func TestZeoBailianTotalOutputBudget(t *testing.T) {
	old := config.ZeoNexusEnabled
	config.ZeoNexusEnabled = true
	defer func() { config.ZeoNexusEnabled = old }()
	for _, host := range []string{"https://llm-demo.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", "https://dashscope.aliyuncs.com/compatible-mode/v1"} {
		a := &Adaptor{}
		a.Init(&meta.Meta{ChannelType: channeltype.OpenAI, BaseURL: host})
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		thinking := true
		r := &model.GeneralOpenAIRequest{Model: "qwen3.8-max", MaxTokens: 256, Stream: true, EnableThinking: &thinking}
		got, err := a.ConvertRequest(c, 1, r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(got)
		var data map[string]any
		json.Unmarshal(body, &data)
		if data["max_completion_tokens"] != float64(256) || data["max_tokens"] != nil || data["enable_thinking"] != true {
			t.Fatalf("unsafe complete-output cap: %s", body)
		}
		if !r.StreamOptions.IncludeUsage {
			t.Fatal("stream has no authoritative usage request")
		}
	}
	a := &Adaptor{ChannelType: channeltype.OpenAI, BaseURL: "http://maizheng.stack.zeotrue.com:8090/v1"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	r := &model.GeneralOpenAIRequest{Model: "qwen3-8b", MaxTokens: 256}
	a.ConvertRequest(c, 1, r)
	if r.MaxTokens != 256 || r.MaxCompletionTokens != nil {
		t.Fatal("GPUStack parameter semantics were changed")
	}
}
