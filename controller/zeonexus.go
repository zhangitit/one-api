package controller

import (
	"bytes"
	"context"
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
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/channeltype"
)

type zeoTenantInput struct {
	Name              string `json:"name"`
	Status            string `json:"status"`
	Revision          uint   `json:"revision"`
	TokenLimit        int64  `json:"token_limit"`
	DailyTokenLimit   int64  `json:"daily_token_limit"`
	MonthlyTokenLimit int64  `json:"monthly_token_limit"`
	RPM               int    `json:"rpm"`
	TPM               int64  `json:"tpm"`
	MaxRequestTokens  int64  `json:"max_request_tokens"`
	AutoPause         bool   `json:"auto_pause"`
}

type zeoCredentialInput struct {
	TenantId          string   `json:"tenant_id"`
	KeyHash           string   `json:"key_hash"`
	KeyPrefix         string   `json:"key_prefix"`
	Name              string   `json:"name"`
	Profile           string   `json:"profile"`
	Status            string   `json:"status"`
	Models            []string `json:"models"`
	AllowedSites      []string `json:"allowed_sites"`
	ExpiresAt         int64    `json:"expires_at"`
	TokenLimit        int64    `json:"token_limit"`
	UsedTokens        int64    `json:"used_tokens"`
	Revision          uint     `json:"revision"`
	DailyTokenLimit   int64    `json:"daily_token_limit"`
	MonthlyTokenLimit int64    `json:"monthly_token_limit"`
	RPM               int      `json:"rpm"`
	TPM               int64    `json:"tpm"`
	MaxRequestTokens  int64    `json:"max_request_tokens"`
	AutoPause         bool     `json:"auto_pause"`
}

type zeoChannelInput struct {
	Name           string            `json:"name"`
	Type           int               `json:"type"`
	BaseURL        string            `json:"base_url"`
	Key            string            `json:"key"`
	Models         []string          `json:"models"`
	ModelMapping   map[string]string `json:"model_mapping"`
	Status         string            `json:"status"`
	Weight         uint              `json:"weight"`
	Priority       int64             `json:"priority"`
	Profile        string            `json:"profile"`
	SiteId         string            `json:"site_id"`
	NodeId         string            `json:"node_id"`
	EndpointId     string            `json:"endpoint_id"`
	MaxConcurrency uint              `json:"max_concurrency"`
	Revision       uint              `json:"revision"`
}

type zeoGrantInput struct {
	TenantId          string   `json:"tenant_id"`
	Profile           string   `json:"profile"`
	Model             string   `json:"model"`
	AllowedSites      []string `json:"allowed_sites"`
	Status            string   `json:"status"`
	ExpiresAt         int64    `json:"expires_at"`
	TokenLimit        int64    `json:"token_limit"`
	UsedTokens        int64    `json:"used_tokens"`
	Revision          uint     `json:"revision"`
	DailyTokenLimit   int64    `json:"daily_token_limit"`
	MonthlyTokenLimit int64    `json:"monthly_token_limit"`
	RPM               int      `json:"rpm"`
	TPM               int64    `json:"tpm"`
	MaxRequestTokens  int64    `json:"max_request_tokens"`
	AutoPause         bool     `json:"auto_pause"`
}

func ZeoNexusHealth(c *gin.Context) {
	sqlDB, err := model.DB.DB()
	if err == nil {
		err = sqlDB.PingContext(c.Request.Context())
	}
	databaseOK := err == nil
	redisOK := true
	if common.RedisEnabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		redisOK = common.RDB != nil && common.RDB.Ping(ctx).Err() == nil
	}
	status := http.StatusOK
	if !databaseOK || !redisOK {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{"status": map[bool]string{true: "ok", false: "degraded"}[databaseOK && redisOK],
		"profile": config.ZeoNexusProfile, "version": "1.1.0", "database": databaseOK,
		"redis_enabled": common.RedisEnabled, "redis": redisOK, "time": time.Now().Unix()})
}

func ZeoNexusReady(c *gin.Context) {
	sqlDB, err := model.DB.DB()
	if err == nil {
		err = sqlDB.PingContext(c.Request.Context())
	}
	if err == nil && common.RedisEnabled {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if common.RDB == nil {
			err = fmt.Errorf("redis is unavailable")
		} else {
			err = common.RDB.Ping(ctx).Err()
		}
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func ZeoNexusUpsertTenant(c *gin.Context) {
	var input zeoTenantInput
	if err := c.ShouldBindJSON(&input); err != nil {
		internalError(c, http.StatusBadRequest, err)
		return
	}
	tenant, err := model.UpsertZeoTenant(model.ZeoTenant{ExternalId: c.Param("id"), Name: input.Name,
		Status: input.Status, Revision: input.Revision, TokenLimit: input.TokenLimit,
		DailyTokenLimit: input.DailyTokenLimit, MonthlyTokenLimit: input.MonthlyTokenLimit,
		RPM: input.RPM, TPM: input.TPM, MaxRequestTokens: input.MaxRequestTokens, AutoPause: input.AutoPause})
	if err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": tenant})
}

func ZeoNexusUpsertCredential(c *gin.Context) {
	var input zeoCredentialInput
	if err := c.ShouldBindJSON(&input); err != nil {
		internalError(c, http.StatusBadRequest, err)
		return
	}
	credential, err := model.UpsertZeoCredential(model.ZeoCredential{ExternalId: c.Param("id"), TenantExternalId: input.TenantId,
		KeyHash: strings.ToLower(input.KeyHash), KeyPrefix: input.KeyPrefix, Name: input.Name, Profile: input.Profile,
		Status: input.Status, Models: strings.Join(input.Models, ","), AllowedSites: strings.Join(input.AllowedSites, ","),
		ExpiresAt: input.ExpiresAt, TokenLimit: input.TokenLimit, UsedTokens: input.UsedTokens,
		DailyTokenLimit: input.DailyTokenLimit, MonthlyTokenLimit: input.MonthlyTokenLimit,
		RPM: input.RPM, TPM: input.TPM, MaxRequestTokens: input.MaxRequestTokens, AutoPause: input.AutoPause,
		Revision: input.Revision})
	if err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"external_id": credential.ExternalId, "revision": credential.Revision, "status": credential.Status}})
}

func ZeoNexusDisableCredential(c *gin.Context) {
	revision, _ := strconv.ParseUint(c.Query("revision"), 10, 32)
	if err := model.DisableZeoCredential(c.Param("id"), uint(revision)); err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func ZeoNexusUpsertGrant(c *gin.Context) {
	var input zeoGrantInput
	if err := c.ShouldBindJSON(&input); err != nil {
		internalError(c, http.StatusBadRequest, err)
		return
	}
	grant, err := model.UpsertZeoGrant(model.ZeoGrant{ExternalId: c.Param("id"), TenantExternalId: input.TenantId,
		Profile: input.Profile, Model: input.Model, AllowedSites: strings.Join(input.AllowedSites, ","), Status: input.Status,
		ExpiresAt: input.ExpiresAt, TokenLimit: input.TokenLimit, UsedTokens: input.UsedTokens,
		DailyTokenLimit: input.DailyTokenLimit, MonthlyTokenLimit: input.MonthlyTokenLimit,
		RPM: input.RPM, TPM: input.TPM, MaxRequestTokens: input.MaxRequestTokens, AutoPause: input.AutoPause,
		Revision: input.Revision})
	if err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": grant})
}

func ZeoNexusDisableGrant(c *gin.Context) {
	revision, _ := strconv.ParseUint(c.Query("revision"), 10, 32)
	if err := model.DisableZeoGrant(c.Param("id"), uint(revision)); err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func ZeoNexusUpsertChannel(c *gin.Context) {
	var input zeoChannelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		internalError(c, http.StatusBadRequest, err)
		return
	}
	if input.Type == 0 {
		input.Type = channeltype.OpenAICompatible
	}
	if err := config.ValidateZeoNexusUpstreamURL(input.BaseURL); err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	mapping, err := json.Marshal(input.ModelMapping)
	if err != nil {
		internalError(c, http.StatusBadRequest, err)
		return
	}
	baseURL, mappingText := strings.TrimRight(input.BaseURL, "/"), string(mapping)
	status := model.ChannelStatusEnabled
	if input.Status != model.ZeoStatusActive {
		status = model.ChannelStatusManuallyDisabled
	}
	channel := &model.Channel{Type: input.Type, Key: input.Key, Status: status, Name: input.Name,
		Weight: &input.Weight, BaseURL: &baseURL, Models: strings.Join(input.Models, ","),
		Group: "zeonexus-" + input.Profile, ModelMapping: &mappingText, Priority: &input.Priority}
	managed, err := model.UpsertZeoChannel(c.Param("id"), input.Revision, input.Profile, input.SiteId, input.NodeId, input.EndpointId, input.MaxConcurrency, channel)
	if err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"external_id": managed.ExternalId, "channel_id": managed.ChannelId, "revision": managed.Revision}})
}

func ZeoNexusDisableChannel(c *gin.Context) {
	revision, _ := strconv.ParseUint(c.Query("revision"), 10, 32)
	if err := model.DisableZeoChannel(c.Param("id"), uint(revision)); err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func ZeoNexusTestChannel(c *gin.Context) {
	var managed model.ZeoManagedChannel
	if err := model.DB.Where("external_id = ?", c.Param("id")).First(&managed).Error; err != nil {
		internalError(c, http.StatusNotFound, err)
		return
	}
	channel, err := model.GetChannelById(managed.ChannelId, true)
	if err != nil {
		internalError(c, http.StatusNotFound, err)
		return
	}
	channel.Key, err = model.RevealZeoChannelKey(channel.Key)
	if err != nil {
		internalError(c, http.StatusInternalServerError, err)
		return
	}
	models := strings.Split(channel.Models, ",")
	if len(models) == 0 || models[0] == "" {
		internalError(c, http.StatusUnprocessableEntity, fmt.Errorf("channel has no model"))
		return
	}
	actual := models[0]
	if mapped := channel.GetModelMapping()[actual]; mapped != "" {
		actual = mapped
	}
	payload, _ := json.Marshal(gin.H{"model": actual, "messages": []gin.H{{"role": "user", "content": "Reply only OK"}}, "max_tokens": 8, "stream": false})
	path := "/v1/chat/completions"
	if channel.Type == channeltype.OpenAICompatible {
		path = "/chat/completions"
	}
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimRight(channel.GetBaseURL(), "/")+path, bytes.NewReader(payload))
	if err != nil {
		internalError(c, http.StatusInternalServerError, err)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+channel.Key)
	started := time.Now()
	response, err := (&http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(request)
	if err != nil {
		internalError(c, http.StatusBadGateway, err)
		return
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		internalError(c, http.StatusBadGateway, fmt.Errorf("upstream returned %d", response.StatusCode))
		return
	}
	var parsed struct {
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Usage.Total <= 0 || parsed.Usage.Total != parsed.Usage.Prompt+parsed.Usage.Completion {
		internalError(c, http.StatusBadGateway, fmt.Errorf("upstream response has no valid usage"))
		return
	}
	channel.UpdateResponseTime(time.Since(started).Milliseconds())
	c.JSON(http.StatusOK, gin.H{"data": gin.H{"healthy": true, "duration_ms": time.Since(started).Milliseconds(), "usage": parsed.Usage}})
}

func ZeoNexusUpsertRoute(c *gin.Context) {
	var route model.ZeoRoute
	if err := c.ShouldBindJSON(&route); err != nil {
		internalError(c, http.StatusBadRequest, err)
		return
	}
	route.ExternalId = c.Param("id")
	result, err := model.UpsertZeoRoute(route)
	if err != nil {
		internalError(c, http.StatusUnprocessableEntity, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func ZeoNexusUsage(c *gin.Context) {
	after, _ := strconv.ParseUint(c.Query("after"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := model.GetZeoUsage(after, limit)
	if err != nil {
		internalError(c, http.StatusInternalServerError, err)
		return
	}
	next := after
	if len(rows) > 0 {
		next = rows[len(rows)-1].Id
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "next_cursor": next, "has_more": len(rows) == limit && limit > 0})
}

func ZeoNexusUsageReconciliation(c *gin.Context) {
	result, err := model.GetZeoUsageReconciliation()
	if err != nil {
		internalError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

func internalError(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"error": err.Error(), "request_id": c.GetString(helper.RequestIdKey)})
}
