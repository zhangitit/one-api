package model

import (
	"context"
	"github.com/songquanpeng/one-api/common/billing"
)

func ReserveZeoMoney(ctx context.Context, credential *ZeoCredential, grant *ZeoGrant, requestID, name string, prompt, completion int64) error {
	_, err := billing.Request(ctx, "reserve", map[string]any{"request_id": requestID, "credential_ref": credential.ExternalId,
		"grant_ref": grant.ExternalId, "tenant_id": credential.TenantExternalId, "model": name,
		"prompt_tokens": prompt, "completion_tokens": completion})
	return err
}

// Finalization is repeatable. If this callback fails, the Console's usage import retries it from the durable usage row.
func FinalizeZeoMoney(requestID string) error {
	var usage ZeoUsage
	if err := DB.Where("request_id = ?", requestID).First(&usage).Error; err != nil {
		return err
	}
	if usage.Status == ZeoUsagePending {
		return nil
	}
	_, err := billing.Request(context.Background(), "finalize", map[string]any{"request_id": requestID, "status": usage.Status,
		"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens,
		"cached_tokens": usage.CachedTokens, "cache_usage_known": usage.CacheUsageKnown})
	return err
}
