package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

const (
	zeoTimestampHeader = "X-Zeo-Timestamp"
	zeoNonceHeader     = "X-Zeo-Nonce"
	zeoSignatureHeader = "X-Zeo-Signature"
)

func ZeoNexusControlAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.ZeoNexusEnabled || len(config.ZeoNexusControlSecret) < 32 {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "ZeoNexus control API is not configured"})
			return
		}
		timestampText := c.GetHeader(zeoTimestampHeader)
		nonce := c.GetHeader(zeoNonceHeader)
		provided := strings.ToLower(strings.TrimSpace(c.GetHeader(zeoSignatureHeader)))
		timestamp, err := strconv.ParseInt(timestampText, 10, 64)
		if err != nil || time.Since(time.Unix(timestamp, 0)).Abs() > config.ZeoNexusSignatureTTL {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "expired or invalid signature timestamp"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(c.Request.Body, (2<<20)+1))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "cannot read request body"})
			return
		}
		if len(body) > 2<<20 {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "control request body is too large"})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		bodyHash := sha256.Sum256(body)
		canonical := strings.Join([]string{c.Request.Method, c.Request.URL.RequestURI(), timestampText, nonce, hex.EncodeToString(bodyHash[:])}, "\n")
		mac := hmac.New(sha256.New, []byte(config.ZeoNexusControlSecret))
		_, _ = mac.Write([]byte(canonical))
		expected := hex.EncodeToString(mac.Sum(nil))
		if len(provided) != len(expected) || !hmac.Equal([]byte(provided), []byte(expected)) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
			return
		}
		if err := model.StoreZeoNonce(nonce, timestamp); err != nil {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{"error": "signature nonce was already used"})
			return
		}
		c.Next()
	}
}

func zeoNexusTokenAuth(c *gin.Context) {
	start := time.Now()
	raw := strings.TrimSpace(c.GetHeader("Authorization"))
	if !strings.HasPrefix(raw, "Bearer ") {
		zeoAbort(c, http.StatusUnauthorized, "invalid_api_key", "未提供 ZeoNexus API 密钥")
		return
	}
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
	wantedPrefix := "sk-nx-agg-"
	if config.ZeoNexusProfile == "inference" {
		wantedPrefix = "sk-nx-cmp-"
	}
	if !strings.HasPrefix(raw, wantedPrefix) {
		zeoAbort(c, http.StatusUnauthorized, "invalid_api_key", "API 密钥与当前网关类型不匹配")
		return
	}
	credential, token, err := model.FindZeoCredential(raw)
	if err != nil {
		zeoAbort(c, http.StatusUnauthorized, "invalid_api_key", "API 密钥无效、已到期或已停用")
		return
	}
	if shouldCheckModel(c) {
		body, readErr := io.ReadAll(io.LimitReader(c.Request.Body, config.ZeoNexusMaxRequestBodyBytes+1))
		if readErr != nil || int64(len(body)) > config.ZeoNexusMaxRequestBodyBytes {
			zeoAbort(c, http.StatusRequestEntityTooLarge, "request_too_large", "请求体超过网关限制")
			return
		}
		c.Set(ctxkey.KeyRequestBody, body)
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
	}
	availableModels := model.GetZeoAvailableModels(credential)
	if c.Request.Method == http.MethodGet {
		c.Set(ctxkey.AvailableModels, strings.Join(availableModels, ","))
	} else {
		c.Set(ctxkey.AvailableModels, credential.Models)
	}
	requestModel, err := getRequestModel(c)
	if err != nil && shouldCheckModel(c) {
		zeoAbort(c, http.StatusBadRequest, "invalid_request", "请求格式不正确")
		return
	}
	if requestModel != "" && credential.Models != "" && !isModelInList(requestModel, credential.Models) {
		zeoAbort(c, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("该密钥无权使用模型：%s", requestModel))
		return
	}
	grant, err := model.FindZeoGrant(credential.TenantExternalId, requestModel, credential.Profile)
	if err != nil && shouldCheckModel(c) {
		zeoAbort(c, http.StatusForbidden, "model_not_allowed", "该客户没有此模型的有效授权")
		return
	}
	if shouldCheckModel(c) {
		expectedMode, modeErr := model.GetZeoRouteMode(requestModel, config.ZeoNexusProfile)
		if modeErr != nil || expectedMode != relaymode.Name(relaymode.GetByPath(c.Request.URL.Path)) {
			zeoAbort(c, http.StatusBadRequest, "model_mode_mismatch", "模型与 API 类型不匹配")
			return
		}
	}
	requestId := c.GetString(helper.RequestIdKey)
	reserve := estimateZeoReservation(c)
	if c.Request.Method == http.MethodGet {
		reserve = 0
	}
	if reserve > config.ZeoNexusMaxReserveTokens {
		zeoAbort(c, http.StatusBadRequest, "max_tokens_exceeded", "请求的最大输出 Token 超过网关限制")
		return
	}
	if grant != nil {
		if policyLimit := model.GetZeoMaxRequestTokens(credential, grant); policyLimit > 0 && reserve > policyLimit {
			zeoAbort(c, http.StatusBadRequest, "max_tokens_exceeded", "请求的最大输出 Token 超过当前额度策略")
			return
		}
	}
	if reserve > 0 {
		if grant == nil {
			zeoAbort(c, http.StatusForbidden, "model_not_allowed", "该客户没有此模型的有效授权")
			return
		}
		if err := model.ReserveZeoUsage(credential, grant, requestId, requestModel, reserve); err != nil {
			if strings.Contains(err.Error(), "per_minute") {
				zeoAbort(c, http.StatusTooManyRequests, "rate_limit_exceeded", "每分钟请求数或 Token 数已达到上限，请稍后重试")
				return
			}
			zeoAbort(c, http.StatusForbidden, "quota_exceeded", "租户、API 密钥或模型额度不足")
			return
		}
	}
	c.Set(ctxkey.RequestModel, requestModel)
	c.Set(ctxkey.Id, token.UserId)
	c.Set(ctxkey.TokenId, token.Id)
	c.Set(ctxkey.TokenName, token.Name)
	c.Set(ctxkey.ZeoCredentialId, credential.Id)
	c.Set(ctxkey.ZeoTenantId, credential.TenantExternalId)
	c.Set(ctxkey.ZeoProfile, credential.Profile)
	allowedSites := credential.AllowedSites
	if grant != nil {
		allowedSites = grant.AllowedSites
	}
	c.Set(ctxkey.ZeoAllowedSites, allowedSites)
	c.Set(ctxkey.ZeoReserved, reserve)
	c.Set(ctxkey.ZeoRequestStart, start)
	c.Next()
	if reserve > 0 {
		status := c.Writer.Status()
		if status >= 400 {
			if c.GetInt(ctxkey.ChannelId) > 0 {
				_ = model.ReconcileZeoUsage(requestId, "upstream_usage_unknown", status, time.Since(start).Milliseconds(), c.GetInt64(ctxkey.ZeoFirstByteMs))
			} else {
				_ = model.FailZeoUsage(requestId, "relay_error", status, time.Since(start).Milliseconds(), c.GetInt64(ctxkey.ZeoFirstByteMs))
			}
		} else {
			// The usage handler normally settles this first; FailZeoUsage is idempotent.
			_ = model.FailZeoUsage(requestId, "usage_missing", status, time.Since(start).Milliseconds(), c.GetInt64(ctxkey.ZeoFirstByteMs))
		}
	}
}

func zeoAbort(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"message": helper.MessageWithRequestId(message, c.GetString(helper.RequestIdKey)),
		"type": "invalid_request_error", "code": code}})
	c.Abort()
}

func estimateZeoReservation(c *gin.Context) int64 {
	body, err := common.GetRequestBody(c)
	if err != nil {
		return config.ZeoNexusDefaultReserveTokens
	}
	var request struct {
		MaxTokens           int64 `json:"max_tokens"`
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
	}
	_ = json.Unmarshal(body, &request)
	output := request.MaxTokens
	if request.MaxCompletionTokens > output {
		output = request.MaxCompletionTokens
	}
	if output <= 0 {
		output = config.ZeoNexusDefaultReserveTokens
	}
	reserve := output + int64(len(body)/4+256)
	return reserve
}

type firstByteWriter struct {
	gin.ResponseWriter
	started time.Time
	ctx     *gin.Context
	wrote   bool
}

func (writer *firstByteWriter) mark() {
	if !writer.wrote {
		writer.wrote = true
		writer.ctx.Set(ctxkey.ZeoFirstByteMs, time.Since(writer.started).Milliseconds())
	}
}

func (writer *firstByteWriter) Write(data []byte) (int, error) {
	writer.mark()
	return writer.ResponseWriter.Write(data)
}

func (writer *firstByteWriter) WriteString(value string) (int, error) {
	writer.mark()
	return writer.ResponseWriter.WriteString(value)
}

func ZeoNexusFirstByte() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.ZeoNexusEnabled {
			c.Next()
			return
		}
		c.Writer = &firstByteWriter{ResponseWriter: c.Writer, started: time.Now(), ctx: c}
		c.Next()
	}
}
