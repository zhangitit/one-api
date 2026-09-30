package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/helper"
)

func RequestId() func(c *gin.Context) {
	return func(c *gin.Context) {
		id := helper.GenRequestID()
		c.Set(helper.RequestIdKey, id)
		ctx := helper.SetRequestID(c.Request.Context(), id)
		c.Request = c.Request.WithContext(ctx)
		if config.ZeoNexusEnabled {
			c.Header("X-ZeoNexus-Request-Id", id)
		} else {
			c.Header(helper.RequestIdKey, id)
		}
		c.Next()
	}
}
