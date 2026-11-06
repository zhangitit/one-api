package helper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// IsAnthropicMessagesPath identifies the client protocol independently of the
// channel's upstream protocol. Token counting uses the same authentication.
func IsAnthropicMessagesPath(path string) bool {
	return path == "/v1/messages" || path == "/v1/messages/count_tokens"
}

func AnthropicError(status int, message, requestID string) gin.H {
	kind := "api_error"
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = "invalid_request_error"
	case http.StatusUnauthorized:
		kind = "authentication_error"
	case http.StatusForbidden:
		kind = "permission_error"
	case http.StatusNotFound:
		kind = "not_found_error"
	case http.StatusRequestEntityTooLarge:
		kind = "request_too_large"
	case http.StatusTooManyRequests:
		kind = "rate_limit_error"
	case 529:
		kind = "overloaded_error"
	}
	return gin.H{"type": "error", "error": gin.H{"type": kind, "message": message}, "request_id": requestID}
}

func WriteAnthropicError(c *gin.Context, status int, message string) {
	if c.Writer.Written() {
		// A failed stream must stay SSE; a JSON response or channel retry would
		// corrupt the event sequence already consumed by the client.
		if c.GetBool("anthropic_stream_error_sent") || !strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "text/event-stream") {
			return
		}
		body, _ := json.Marshal(AnthropicError(status, message, c.GetString(RequestIdKey)))
		_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", body)
		c.Writer.Flush()
		return
	}
	c.Header("Content-Type", "application/json")
	c.JSON(status, AnthropicError(status, message, c.GetString(RequestIdKey)))
}
