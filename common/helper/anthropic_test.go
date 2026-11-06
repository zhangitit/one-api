package helper

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAnthropicErrorsFollowClientProtocol(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 429, 500, 529} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set(RequestIdKey, "request-1")
		c.Header("Content-Type", "text/event-stream")
		WriteAnthropicError(c, status, "failure")
		if recorder.Code != status || recorder.Header().Get("Content-Type") != "application/json" || !strings.Contains(recorder.Body.String(), `"type":"error"`) || !strings.Contains(recorder.Body.String(), `"request_id":"request-1"`) {
			t.Fatalf("invalid envelope: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestAnthropicStreamFailureStaysSSE(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Header("Content-Type", "text/event-stream")
	c.Writer.Write([]byte("event: message_start\ndata: {}\n\n"))
	WriteAnthropicError(c, 502, "interrupted")
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "event: error\ndata: ") || !recorder.Flushed {
		t.Fatalf("stream failure corrupted protocol: %d %s", recorder.Code, recorder.Body.String())
	}
	before := recorder.Body.String()
	c.Set("anthropic_stream_error_sent", true)
	WriteAnthropicError(c, 502, "duplicate")
	if recorder.Body.String() != before {
		t.Fatal("duplicated upstream stream error")
	}
}
