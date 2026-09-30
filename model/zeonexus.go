package model

import (
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ZeoStatusActive   = "active"
	ZeoStatusDisabled = "disabled"
	ZeoUsagePending   = "pending"
	ZeoUsageSuccess   = "success"
	ZeoUsageFailed    = "failed"
	ZeoUsageReconcile = "reconcile_required"
)

type ZeoTenant struct {
	Id                uint64 `json:"id"`
	ExternalId        string `json:"external_id" gorm:"size:80;uniqueIndex"`
	UserId            int    `json:"user_id" gorm:"uniqueIndex"`
	Name              string `json:"name" gorm:"size:160"`
	Status            string `json:"status" gorm:"size:20;index"`
	TokenLimit        int64  `json:"token_limit"`
	DailyTokenLimit   int64  `json:"daily_token_limit"`
	MonthlyTokenLimit int64  `json:"monthly_token_limit"`
	RPM               int    `json:"rpm"`
	TPM               int64  `json:"tpm"`
	MaxRequestTokens  int64  `json:"max_request_tokens"`
	AutoPause         bool   `json:"auto_pause"`
	Revision          uint   `json:"revision"`
	UpdatedAt         int64  `json:"updated_at"`
}

type ZeoCredential struct {
	Id                uint64 `json:"id"`
	ExternalId        string `json:"external_id" gorm:"size:80;uniqueIndex"`
	TenantExternalId  string `json:"tenant_external_id" gorm:"size:80;index"`
	TokenId           int    `json:"token_id" gorm:"uniqueIndex"`
	KeyHash           string `json:"-" gorm:"type:char(64);uniqueIndex"`
	KeyPrefix         string `json:"key_prefix" gorm:"size:24;index"`
	Name              string `json:"name" gorm:"size:80"`
	Profile           string `json:"profile" gorm:"size:20;index"`
	Status            string `json:"status" gorm:"size:20;index"`
	Models            string `json:"models" gorm:"type:text"`
	AllowedSites      string `json:"allowed_sites" gorm:"type:text"`
	ExpiresAt         int64  `json:"expires_at" gorm:"index"`
	TokenLimit        int64  `json:"token_limit"`
	UsedTokens        int64  `json:"used_tokens"`
	ReservedTokens    int64  `json:"reserved_tokens"`
	DailyTokenLimit   int64  `json:"daily_token_limit"`
	MonthlyTokenLimit int64  `json:"monthly_token_limit"`
	RPM               int    `json:"rpm"`
	TPM               int64  `json:"tpm"`
	MaxRequestTokens  int64  `json:"max_request_tokens"`
	AutoPause         bool   `json:"auto_pause"`
	Revision          uint   `json:"revision"`
	UpdatedAt         int64  `json:"updated_at"`
}

type ZeoGrant struct {
	Id                uint64 `json:"id"`
	ExternalId        string `json:"external_id" gorm:"size:80;uniqueIndex"`
	TenantExternalId  string `json:"tenant_external_id" gorm:"size:80;index:idx_zeo_grant_lookup,priority:1"`
	Profile           string `json:"profile" gorm:"size:20;index:idx_zeo_grant_lookup,priority:2"`
	Model             string `json:"model" gorm:"size:180;index:idx_zeo_grant_lookup,priority:3"`
	AllowedSites      string `json:"allowed_sites" gorm:"type:text"`
	Status            string `json:"status" gorm:"size:20;index"`
	ExpiresAt         int64  `json:"expires_at" gorm:"index"`
	TokenLimit        int64  `json:"token_limit"`
	UsedTokens        int64  `json:"used_tokens"`
	ReservedTokens    int64  `json:"reserved_tokens"`
	DailyTokenLimit   int64  `json:"daily_token_limit"`
	MonthlyTokenLimit int64  `json:"monthly_token_limit"`
	RPM               int    `json:"rpm"`
	TPM               int64  `json:"tpm"`
	MaxRequestTokens  int64  `json:"max_request_tokens"`
	AutoPause         bool   `json:"auto_pause"`
	Revision          uint   `json:"revision"`
	UpdatedAt         int64  `json:"updated_at"`
}

type ZeoManagedChannel struct {
	Id             uint64 `json:"id"`
	ExternalId     string `json:"external_id" gorm:"size:80;uniqueIndex"`
	ChannelId      int    `json:"channel_id" gorm:"uniqueIndex"`
	Profile        string `json:"profile" gorm:"size:20;index"`
	SiteId         string `json:"site_id" gorm:"size:80;index"`
	NodeId         string `json:"node_id" gorm:"size:80"`
	EndpointId     string `json:"endpoint_id" gorm:"size:80"`
	MaxConcurrency uint   `json:"max_concurrency"`
	ActiveRequests uint   `json:"active_requests"`
	Revision       uint   `json:"revision"`
	UpdatedAt      int64  `json:"updated_at"`
}

type ZeoRoute struct {
	Id         uint64 `json:"id"`
	ExternalId string `json:"external_id" gorm:"size:80;uniqueIndex"`
	Profile    string `json:"profile" gorm:"size:20;index"`
	Model      string `json:"model" gorm:"size:180;index"`
	Mode       string `json:"mode" gorm:"size:20;default:chat"`
	ChannelIds string `json:"channel_ids" gorm:"type:text"`
	Strategy   string `json:"strategy" gorm:"size:30"`
	Status     string `json:"status" gorm:"size:20;index"`
	Revision   uint   `json:"revision"`
	UpdatedAt  int64  `json:"updated_at"`
}

type ZeoUsage struct {
	Id                 uint64 `json:"id"`
	RequestId          string `json:"request_id" gorm:"size:80;uniqueIndex"`
	CredentialId       uint64 `json:"credential_id" gorm:"index"`
	CredentialRef      string `json:"credential_ref" gorm:"size:80;index"`
	GrantId            uint64 `json:"grant_id" gorm:"index"`
	GrantRef           string `json:"grant_ref" gorm:"size:80;index"`
	TenantExternalId   string `json:"tenant_id" gorm:"size:80;index"`
	Profile            string `json:"profile" gorm:"size:20;index"`
	Model              string `json:"model" gorm:"size:180;index"`
	UpstreamModel      string `json:"upstream_model" gorm:"size:180"`
	ChannelRef         string `json:"channel_id" gorm:"size:80"`
	SiteId             string `json:"site_id" gorm:"size:80;index"`
	NodeId             string `json:"node_id" gorm:"size:80"`
	EndpointId         string `json:"endpoint_id" gorm:"size:80"`
	Status             string `json:"status" gorm:"size:20;index"`
	ReservedTokens     int64  `json:"reserved_tokens"`
	PromptTokens       int    `json:"prompt_tokens"`
	CompletionTokens   int    `json:"completion_tokens"`
	TotalTokens        int    `json:"total_tokens"`
	DurationMs         int64  `json:"duration_ms"`
	FirstByteMs        int64  `json:"first_byte_ms"`
	IsStream           bool   `json:"is_stream"`
	HTTPStatus         int    `json:"http_status"`
	ErrorCode          string `json:"error_code" gorm:"size:80"`
	ClientDisconnected bool   `json:"client_disconnected"`
	CreatedAt          int64  `json:"created_at" gorm:"index"`
	CompletedAt        int64  `json:"completed_at"`
}

type ZeoNonce struct {
	Id        uint64 `json:"id"`
	Nonce     string `json:"nonce" gorm:"size:80;uniqueIndex"`
	CreatedAt int64  `json:"created_at" gorm:"index"`
}

type ZeoUsageBalance struct {
	ExternalId     string `json:"external_id"`
	UsedTokens     int64  `json:"used_tokens"`
	ReservedTokens int64  `json:"reserved_tokens"`
}

type ZeoUsageReconciliation struct {
	Profile     string            `json:"profile"`
	MaxUsageId  uint64            `json:"max_usage_id"`
	Credentials []ZeoUsageBalance `json:"credentials"`
	Grants      []ZeoUsageBalance `json:"grants"`
}

func MigrateZeoNexus() error {
	return DB.AutoMigrate(&ZeoTenant{}, &ZeoCredential{}, &ZeoGrant{}, &ZeoManagedChannel{}, &ZeoRoute{}, &ZeoUsage{}, &ZeoNonce{})
}

func HashZeoKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func protectZeoSecret(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "zeo:v1:") {
		return raw, nil
	}
	key := sha256.Sum256([]byte(config.ZeoNexusMasterKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(crand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(raw), []byte("zeonexus-channel-v1"))
	return "zeo:v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func RevealZeoChannelKey(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !strings.HasPrefix(value, "zeo:v1:") {
		return "", errors.New("channel secret is not encrypted")
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "zeo:v1:"))
	if err != nil {
		return "", errors.New("channel secret is corrupt")
	}
	key := sha256.Sum256([]byte(config.ZeoNexusMasterKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(payload) < gcm.NonceSize() {
		return "", errors.New("channel secret is corrupt")
	}
	plain, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], []byte("zeonexus-channel-v1"))
	if err != nil {
		return "", errors.New("channel secret cannot be decrypted")
	}
	return string(plain), nil
}

func UpsertZeoTenant(input ZeoTenant) (*ZeoTenant, error) {
	if input.ExternalId == "" || input.Revision == 0 {
		return nil, errors.New("external_id and revision are required")
	}
	if input.Status != ZeoStatusActive && input.Status != ZeoStatusDisabled {
		return nil, errors.New("invalid tenant status")
	}
	if input.RPM < 0 || input.TPM < 0 || input.MaxRequestTokens < 0 || input.TokenLimit < 0 || input.DailyTokenLimit < 0 || input.MonthlyTokenLimit < 0 {
		return nil, errors.New("tenant quota policy cannot be negative")
	}
	var tenant ZeoTenant
	err := DB.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("external_id = ?", input.ExternalId).First(&tenant).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if tenant.Id != 0 && tenant.Revision > input.Revision {
			return nil
		}
		if tenant.UserId == 0 {
			user, createErr := createZeoUser(tx, input.ExternalId, input.Name)
			if createErr != nil {
				return createErr
			}
			tenant.UserId = user.Id
		}
		tenant.ExternalId, tenant.Name, tenant.Status = input.ExternalId, input.Name, input.Status
		tenant.TokenLimit, tenant.DailyTokenLimit, tenant.MonthlyTokenLimit = input.TokenLimit, input.DailyTokenLimit, input.MonthlyTokenLimit
		tenant.RPM, tenant.TPM, tenant.MaxRequestTokens, tenant.AutoPause = input.RPM, input.TPM, input.MaxRequestTokens, input.AutoPause
		tenant.Revision, tenant.UpdatedAt = input.Revision, time.Now().Unix()
		return tx.Save(&tenant).Error
	})
	return &tenant, err
}

func createZeoUser(tx *gorm.DB, externalId, name string) (*User, error) {
	digest := sha256.Sum256([]byte(config.ZeoNexusProfile + ":" + externalId))
	username := "nx" + hex.EncodeToString(digest[:])[:10]
	var existing User
	if err := tx.Where("username = ?", username).First(&existing).Error; err == nil {
		return &existing, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	password, err := common.Password2Hash(uuid.NewString() + uuid.NewString())
	if err != nil {
		return nil, err
	}
	user := &User{Username: username, Password: password, DisplayName: name, Role: RoleCommonUser,
		Status: UserStatusEnabled, AccessToken: strings.ReplaceAll(uuid.NewString(), "-", ""),
		Quota: 9_000_000_000_000_000, Group: "zeonexus-" + config.ZeoNexusProfile,
		AffCode: strings.ReplaceAll(uuid.NewString(), "-", "")[:32]}
	if err := tx.Create(user).Error; err != nil {
		return nil, err
	}
	return user, nil
}

func UpsertZeoCredential(input ZeoCredential) (*ZeoCredential, error) {
	if input.ExternalId == "" || input.TenantExternalId == "" || len(input.KeyHash) != 64 || input.Revision == 0 {
		return nil, errors.New("external_id, tenant_id, key_hash and revision are required")
	}
	if input.Profile != config.ZeoNexusProfile {
		return nil, fmt.Errorf("credential profile %q does not match gateway profile %q", input.Profile, config.ZeoNexusProfile)
	}
	if input.Status != ZeoStatusActive && input.Status != ZeoStatusDisabled {
		return nil, errors.New("invalid credential status")
	}
	if input.TokenLimit < -1 || input.RPM < 0 || input.TPM < 0 || input.MaxRequestTokens < 0 || input.DailyTokenLimit < 0 || input.MonthlyTokenLimit < 0 {
		return nil, errors.New("credential quota policy is invalid")
	}
	var result ZeoCredential
	err := DB.Transaction(func(tx *gorm.DB) error {
		var tenant ZeoTenant
		if err := tx.Where("external_id = ?", input.TenantExternalId).First(&tenant).Error; err != nil {
			return fmt.Errorf("tenant is not provisioned: %w", err)
		}
		if tenant.Status != ZeoStatusActive && input.Status == ZeoStatusActive {
			return errors.New("tenant is disabled")
		}
		err := tx.Where("external_id = ?", input.ExternalId).First(&result).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if result.Id != 0 && result.Revision > input.Revision {
			return nil
		}
		models := input.Models
		status := TokenStatusDisabled
		if input.Status == ZeoStatusActive {
			status = TokenStatusEnabled
		}
		expiry := input.ExpiresAt
		if expiry == 0 {
			expiry = -1
		}
		var token Token
		if result.TokenId != 0 {
			if err := tx.First(&token, result.TokenId).Error; err != nil {
				return err
			}
			token.Name, token.Status, token.Models, token.ExpiredTime = input.Name, status, &models, expiry
			if err := tx.Model(&token).Select("name", "status", "models", "expired_time").Updates(&token).Error; err != nil {
				return err
			}
		} else {
			internalKey := HashZeoKey(input.ExternalId + uuid.NewString())[:48]
			token = Token{UserId: tenant.UserId, Key: internalKey, Status: status, Name: input.Name,
				CreatedTime: helper.GetTimestamp(), AccessedTime: helper.GetTimestamp(), ExpiredTime: expiry,
				RemainQuota: 9_000_000_000_000_000, UnlimitedQuota: true, Models: &models}
			if err := tx.Create(&token).Error; err != nil {
				return err
			}
			result.TokenId = token.Id
		}
		used, reserved := result.UsedTokens, result.ReservedTokens
		if result.Id == 0 {
			used = input.UsedTokens
		}
		result.ExternalId, result.TenantExternalId, result.KeyHash = input.ExternalId, input.TenantExternalId, strings.ToLower(input.KeyHash)
		result.KeyPrefix, result.Name, result.Profile, result.Status = input.KeyPrefix, input.Name, input.Profile, input.Status
		result.Models, result.AllowedSites, result.ExpiresAt = input.Models, input.AllowedSites, input.ExpiresAt
		result.TokenLimit, result.Revision, result.UpdatedAt = input.TokenLimit, input.Revision, time.Now().Unix()
		result.DailyTokenLimit, result.MonthlyTokenLimit = input.DailyTokenLimit, input.MonthlyTokenLimit
		result.RPM, result.TPM, result.MaxRequestTokens, result.AutoPause = input.RPM, input.TPM, input.MaxRequestTokens, input.AutoPause
		result.UsedTokens, result.ReservedTokens = used, reserved
		return tx.Save(&result).Error
	})
	return &result, err
}

func DisableZeoCredential(externalId string, revision uint) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var credential ZeoCredential
		if err := tx.Where("external_id = ?", externalId).First(&credential).Error; err != nil {
			return err
		}
		if credential.Revision > revision {
			return nil
		}
		credential.Status, credential.Revision, credential.UpdatedAt = ZeoStatusDisabled, revision, time.Now().Unix()
		if err := tx.Save(&credential).Error; err != nil {
			return err
		}
		return tx.Model(&Token{}).Where("id = ?", credential.TokenId).Update("status", TokenStatusDisabled).Error
	})
}

func UpsertZeoGrant(input ZeoGrant) (*ZeoGrant, error) {
	if input.ExternalId == "" || input.TenantExternalId == "" || input.Model == "" || input.Revision == 0 {
		return nil, errors.New("external_id, tenant_id, model and revision are required")
	}
	if input.Profile != config.ZeoNexusProfile {
		return nil, fmt.Errorf("grant profile %q does not match gateway profile %q", input.Profile, config.ZeoNexusProfile)
	}
	if input.Status != ZeoStatusActive && input.Status != ZeoStatusDisabled {
		return nil, errors.New("invalid grant status")
	}
	if input.TokenLimit < -1 || input.RPM < 0 || input.TPM < 0 || input.MaxRequestTokens < 0 || input.DailyTokenLimit < 0 || input.MonthlyTokenLimit < 0 {
		return nil, errors.New("grant quota policy is invalid")
	}
	var current ZeoGrant
	err := DB.Where("external_id = ?", input.ExternalId).First(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if current.Id != 0 && current.Revision > input.Revision {
		return &current, nil
	}
	input.Id = current.Id
	if current.Id != 0 {
		input.UsedTokens, input.ReservedTokens = current.UsedTokens, current.ReservedTokens
	}
	input.UpdatedAt = time.Now().Unix()
	return &input, DB.Save(&input).Error
}

func DisableZeoGrant(externalId string, revision uint) error {
	return DB.Model(&ZeoGrant{}).Where("external_id = ? AND revision <= ?", externalId, revision).
		Updates(map[string]any{"status": ZeoStatusDisabled, "revision": revision, "updated_at": time.Now().Unix()}).Error
}

func FindZeoCredential(rawKey string) (*ZeoCredential, *Token, error) {
	var credential ZeoCredential
	if err := DB.Where("key_hash = ?", HashZeoKey(rawKey)).First(&credential).Error; err != nil {
		return nil, nil, err
	}
	if credential.Status != ZeoStatusActive || credential.Profile != config.ZeoNexusProfile {
		return nil, nil, errors.New("credential is disabled or belongs to another gateway")
	}
	if credential.ExpiresAt > 0 && credential.ExpiresAt <= time.Now().Unix() {
		return nil, nil, errors.New("credential has expired")
	}
	var tenant ZeoTenant
	if err := DB.Where("external_id = ? AND status = ?", credential.TenantExternalId, ZeoStatusActive).First(&tenant).Error; err != nil {
		return nil, nil, errors.New("tenant is disabled")
	}
	var token Token
	if err := DB.First(&token, credential.TokenId).Error; err != nil {
		return nil, nil, err
	}
	return &credential, &token, nil
}

func FindZeoGrant(tenantExternalId, modelName, profile string) (*ZeoGrant, error) {
	var grant ZeoGrant
	err := DB.Where("tenant_external_id = ? AND profile = ? AND model = ? AND status = ?", tenantExternalId, profile, modelName, ZeoStatusActive).
		Where("expires_at = 0 OR expires_at > ?", time.Now().Unix()).Order("revision desc, id desc").First(&grant).Error
	if err != nil {
		return nil, errors.New("model grant is unavailable")
	}
	return &grant, nil
}

func GetZeoAvailableModels(credential *ZeoCredential) []string {
	result := make([]string, 0)
	for _, modelName := range splitCSV(credential.Models) {
		grant, err := FindZeoGrant(credential.TenantExternalId, modelName, credential.Profile)
		if err != nil {
			continue
		}
		if _, err = GetZeoSatisfiedChannel(modelName, credential.Profile, grant.AllowedSites, 0); err == nil {
			result = append(result, modelName)
		}
	}
	return result
}

func GetZeoMaxRequestTokens(credential *ZeoCredential, grant *ZeoGrant) int64 {
	limits := []int64{credential.MaxRequestTokens, grant.MaxRequestTokens}
	var tenant ZeoTenant
	if err := DB.Where("external_id = ?", credential.TenantExternalId).First(&tenant).Error; err == nil {
		limits = append(limits, tenant.MaxRequestTokens)
	}
	var result int64
	for _, limit := range limits {
		if limit > 0 && (result == 0 || limit < result) {
			result = limit
		}
	}
	return result
}

func zeoWindowUsage(tx *gorm.DB, field string, value any, since int64) (int64, int64, error) {
	var row struct {
		Requests int64
		Tokens   int64
	}
	err := tx.Model(&ZeoUsage{}).Where(field+" = ? AND created_at >= ?", value, since).
		Select("COUNT(*) AS requests, COALESCE(SUM(CASE WHEN status = ? THEN reserved_tokens ELSE total_tokens END),0) AS tokens", ZeoUsagePending).
		Scan(&row).Error
	return row.Requests, row.Tokens, err
}

func checkZeoWindowPolicy(tx *gorm.DB, field string, value any, rpm int, tpm, daily, monthly, amount int64, now time.Time) error {
	requests, tokens, err := zeoWindowUsage(tx, field, value, now.Truncate(time.Minute).Unix())
	if err != nil {
		return err
	}
	if rpm > 0 && requests+1 > int64(rpm) {
		return errors.New("requests_per_minute_exceeded")
	}
	if tpm > 0 && tokens+amount > tpm {
		return errors.New("tokens_per_minute_exceeded")
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	_, tokens, err = zeoWindowUsage(tx, field, value, dayStart)
	if err != nil {
		return err
	}
	if daily > 0 && tokens+amount > daily {
		return errors.New("daily_quota_exceeded")
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	_, tokens, err = zeoWindowUsage(tx, field, value, monthStart)
	if err != nil {
		return err
	}
	if monthly > 0 && tokens+amount > monthly {
		return errors.New("monthly_quota_exceeded")
	}
	return nil
}

func ReserveZeoUsage(credential *ZeoCredential, grant *ZeoGrant, requestId, model string, amount int64) error {
	if amount < 1 {
		amount = 1
	}
	var policyError error
	err := DB.Transaction(func(tx *gorm.DB) error {
		var tenant ZeoTenant
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("external_id = ? AND status = ?", credential.TenantExternalId, ZeoStatusActive).First(&tenant).Error; err != nil {
			return errors.New("tenant quota is unavailable")
		}
		now := time.Now()
		if err := checkZeoWindowPolicy(tx, "tenant_external_id", credential.TenantExternalId,
			tenant.RPM, tenant.TPM, tenant.DailyTokenLimit, tenant.MonthlyTokenLimit, amount, now); err != nil {
			if tenant.AutoPause {
				if updateErr := tx.Model(&tenant).Updates(map[string]any{
					"status": ZeoStatusDisabled, "revision": gorm.Expr("revision + 1"), "updated_at": now.Unix(),
				}).Error; updateErr != nil {
					return updateErr
				}
				policyError = err
				return nil
			}
			return err
		}
		if tenant.TokenLimit > 0 {
			_, total, err := zeoWindowUsage(tx, "tenant_external_id", credential.TenantExternalId, 0)
			if err != nil {
				return err
			}
			if total+amount > tenant.TokenLimit {
				if tenant.AutoPause {
					if updateErr := tx.Model(&tenant).Updates(map[string]any{
						"status": ZeoStatusDisabled, "revision": gorm.Expr("revision + 1"), "updated_at": now.Unix(),
					}).Error; updateErr != nil {
						return updateErr
					}
					policyError = errors.New("tenant_quota_exceeded")
					return nil
				}
				return errors.New("tenant_quota_exceeded")
			}
		}
		if err := checkZeoWindowPolicy(tx, "credential_id", credential.Id, credential.RPM, credential.TPM,
			credential.DailyTokenLimit, credential.MonthlyTokenLimit, amount, now); err != nil {
			if credential.AutoPause {
				if updateErr := tx.Model(&ZeoCredential{}).Where("id = ?", credential.Id).Updates(map[string]any{
					"status": ZeoStatusDisabled, "revision": gorm.Expr("revision + 1"), "updated_at": now.Unix(),
				}).Error; updateErr != nil {
					return updateErr
				}
				if updateErr := tx.Model(&Token{}).Where("id = ?", credential.TokenId).Update("status", TokenStatusDisabled).Error; updateErr != nil {
					return updateErr
				}
				policyError = err
				return nil
			}
			return err
		}
		if err := checkZeoWindowPolicy(tx, "grant_id", grant.Id, grant.RPM, grant.TPM,
			grant.DailyTokenLimit, grant.MonthlyTokenLimit, amount, now); err != nil {
			if grant.AutoPause {
				if updateErr := tx.Model(&ZeoGrant{}).Where("id = ?", grant.Id).Updates(map[string]any{
					"status": ZeoStatusDisabled, "revision": gorm.Expr("revision + 1"), "updated_at": now.Unix(),
				}).Error; updateErr != nil {
					return updateErr
				}
				policyError = err
				return nil
			}
			return err
		}
		result := tx.Model(&ZeoCredential{}).Where("id = ? AND status = ? AND (token_limit = 0 OR used_tokens + reserved_tokens + ? <= token_limit)", credential.Id, ZeoStatusActive, amount).
			Updates(map[string]any{"reserved_tokens": gorm.Expr("reserved_tokens + ?", amount), "updated_at": time.Now().Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("token quota is insufficient")
		}
		result = tx.Model(&ZeoGrant{}).Where("id = ? AND status = ? AND (token_limit = 0 OR used_tokens + reserved_tokens + ? <= token_limit)", grant.Id, ZeoStatusActive, amount).
			Updates(map[string]any{"reserved_tokens": gorm.Expr("reserved_tokens + ?", amount), "updated_at": time.Now().Unix()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("model grant quota is insufficient")
		}
		usage := ZeoUsage{RequestId: requestId, CredentialId: credential.Id, CredentialRef: credential.ExternalId,
			GrantId: grant.Id, GrantRef: grant.ExternalId, TenantExternalId: credential.TenantExternalId, Profile: credential.Profile, Model: model,
			Status: ZeoUsagePending, ReservedTokens: amount, CreatedAt: time.Now().Unix()}
		if err := tx.Create(&usage).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if err.Error() == "token quota is insufficient" && credential.AutoPause {
			_ = DB.Transaction(func(tx *gorm.DB) error {
				if updateErr := tx.Model(&ZeoCredential{}).Where("id = ?", credential.Id).Updates(map[string]any{
					"status": ZeoStatusDisabled, "revision": gorm.Expr("revision + 1"), "updated_at": time.Now().Unix(),
				}).Error; updateErr != nil {
					return updateErr
				}
				return tx.Model(&Token{}).Where("id = ?", credential.TokenId).Update("status", TokenStatusDisabled).Error
			})
		}
		if err.Error() == "model grant quota is insufficient" && grant.AutoPause {
			_ = DB.Model(&ZeoGrant{}).Where("id = ?", grant.Id).Updates(map[string]any{
				"status": ZeoStatusDisabled, "revision": gorm.Expr("revision + 1"), "updated_at": time.Now().Unix(),
			}).Error
		}
		return err
	}
	return policyError
}

func CompleteZeoUsage(requestId, model, upstreamModel string, channelId, prompt, completion int, durationMs, firstByteMs int64, isStream, clientDisconnected bool) error {
	total := prompt + completion
	return DB.Transaction(func(tx *gorm.DB) error {
		var usage ZeoUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&usage).Error; err != nil {
			return err
		}
		if usage.Status != ZeoUsagePending {
			return nil
		}
		if err := tx.Model(&ZeoCredential{}).Where("id = ?", usage.CredentialId).Updates(map[string]any{
			"reserved_tokens": gorm.Expr("CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END", usage.ReservedTokens, usage.ReservedTokens),
			"used_tokens":     gorm.Expr("used_tokens + ?", total), "updated_at": time.Now().Unix(),
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&ZeoGrant{}).Where("id = ?", usage.GrantId).Updates(map[string]any{
			"reserved_tokens": gorm.Expr("CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END", usage.ReservedTokens, usage.ReservedTokens),
			"used_tokens":     gorm.Expr("used_tokens + ?", total), "updated_at": time.Now().Unix(),
		}).Error; err != nil {
			return err
		}
		var managed ZeoManagedChannel
		_ = tx.Where("channel_id = ?", channelId).First(&managed).Error
		updates := map[string]any{"status": ZeoUsageSuccess, "model": model, "upstream_model": upstreamModel,
			"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": total,
			"duration_ms": durationMs, "first_byte_ms": firstByteMs, "is_stream": isStream, "http_status": 200,
			"channel_ref": managed.ExternalId, "site_id": managed.SiteId, "node_id": managed.NodeId,
			"endpoint_id": managed.EndpointId, "client_disconnected": clientDisconnected, "completed_at": time.Now().Unix()}
		return tx.Model(&usage).Updates(updates).Error
	})
}

// ReconcileZeoUsage conservatively charges the reservation when the upstream was contacted
// but no trustworthy usage was returned. This avoids silently refunding potentially billed work.
func ReconcileZeoUsage(requestId, code string, httpStatus int, durationMs, firstByteMs int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var usage ZeoUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&usage).Error; err != nil {
			return err
		}
		if usage.Status != ZeoUsagePending {
			return nil
		}
		for _, target := range []any{&ZeoCredential{}, &ZeoGrant{}} {
			id := usage.CredentialId
			if _, ok := target.(*ZeoGrant); ok {
				id = usage.GrantId
			}
			if err := tx.Model(target).Where("id = ?", id).Updates(map[string]any{
				"reserved_tokens": gorm.Expr("CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END", usage.ReservedTokens, usage.ReservedTokens),
				"used_tokens":     gorm.Expr("used_tokens + ?", usage.ReservedTokens), "updated_at": time.Now().Unix(),
			}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&usage).Updates(map[string]any{"status": ZeoUsageReconcile, "total_tokens": usage.ReservedTokens,
			"error_code": code, "http_status": httpStatus, "duration_ms": durationMs, "first_byte_ms": firstByteMs,
			"completed_at": time.Now().Unix()}).Error
	})
}

func FailZeoUsage(requestId, code string, httpStatus int, durationMs, firstByteMs int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var usage ZeoUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("request_id = ?", requestId).First(&usage).Error; err != nil {
			return err
		}
		if usage.Status != ZeoUsagePending {
			return nil
		}
		if err := tx.Model(&ZeoCredential{}).Where("id = ?", usage.CredentialId).Update("reserved_tokens",
			gorm.Expr("CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END", usage.ReservedTokens, usage.ReservedTokens)).Error; err != nil {
			return err
		}
		if err := tx.Model(&ZeoGrant{}).Where("id = ?", usage.GrantId).Update("reserved_tokens",
			gorm.Expr("CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END", usage.ReservedTokens, usage.ReservedTokens)).Error; err != nil {
			return err
		}
		return tx.Model(&usage).Updates(map[string]any{"status": ZeoUsageFailed, "error_code": code,
			"http_status": httpStatus, "duration_ms": durationMs, "first_byte_ms": firstByteMs, "completed_at": time.Now().Unix()}).Error
	})
}

func GetZeoSatisfiedChannel(modelName, profile, allowedSites string, excludeChannelId int) (*Channel, error) {
	var route ZeoRoute
	if err := DB.Where("profile = ? AND model = ? AND status = ?", profile, modelName, ZeoStatusActive).
		Order("revision desc, id desc").First(&route).Error; err != nil {
		return nil, errors.New("model route is unavailable")
	}
	query := DB.Table("channels c").Select("c.*").Joins("JOIN zeo_managed_channels z ON z.channel_id = c.id").
		Joins("JOIN abilities a ON a.channel_id = c.id").Where("z.profile = ? AND c.status = ? AND a.model = ? AND a.enabled = ?", profile, ChannelStatusEnabled, modelName, true).
		Where("z.max_concurrency = 0 OR z.active_requests < z.max_concurrency")
	if channelRefs := splitCSV(route.ChannelIds); len(channelRefs) > 0 {
		query = query.Where("z.external_id IN ?", channelRefs)
	} else {
		return nil, errors.New("model route has no channels")
	}
	if allowedSites != "" {
		sites := splitCSV(allowedSites)
		if len(sites) > 0 {
			query = query.Where("z.site_id IN ?", sites)
		}
	}
	if excludeChannelId > 0 {
		query = query.Where("c.id <> ?", excludeChannelId)
	}
	var channels []Channel
	err := query.Order("a.priority desc, c.response_time asc, c.id asc").Limit(100).Find(&channels).Error
	if err == nil && len(channels) == 0 {
		err = gorm.ErrRecordNotFound
	}
	var channel Channel
	if err == nil {
		highest := channels[0].GetPriority()
		eligible := make([]Channel, 0, len(channels))
		var total int64
		for _, candidate := range channels {
			if candidate.GetPriority() != highest {
				break
			}
			weight := int64(1)
			if candidate.Weight != nil && *candidate.Weight > 0 {
				weight = int64(*candidate.Weight)
			}
			total += weight
			eligible = append(eligible, candidate)
		}
		pick, pickErr := crand.Int(crand.Reader, big.NewInt(total))
		if pickErr != nil {
			err = pickErr
		} else {
			selected := pick.Int64()
			for _, candidate := range eligible {
				weight := int64(1)
				if candidate.Weight != nil && *candidate.Weight > 0 {
					weight = int64(*candidate.Weight)
				}
				if selected < weight {
					channel = candidate
					break
				}
				selected -= weight
			}
		}
	}
	if err == nil {
		err = config.ValidateZeoNexusUpstreamURL(channel.GetBaseURL())
	}
	if err == nil {
		channel.Key, err = RevealZeoChannelKey(channel.Key)
	}
	return &channel, err
}

func AcquireZeoChannel(channelId int) bool {
	result := DB.Model(&ZeoManagedChannel{}).Where("channel_id = ? AND (max_concurrency = 0 OR active_requests < max_concurrency)", channelId).
		Update("active_requests", gorm.Expr("active_requests + 1"))
	return result.Error == nil && result.RowsAffected == 1
}

func ReleaseZeoChannel(channelId int) {
	_ = DB.Model(&ZeoManagedChannel{}).Where("channel_id = ? AND active_requests > 0", channelId).
		Update("active_requests", gorm.Expr("active_requests - 1")).Error
}

func splitCSV(value string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func GetZeoUsage(after uint64, limit int) ([]ZeoUsage, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	var rows []ZeoUsage
	err := DB.Where("id > ? AND status <> ?", after, ZeoUsagePending).Order("id asc").Limit(limit).Find(&rows).Error
	return rows, err
}

func GetZeoUsageReconciliation() (*ZeoUsageReconciliation, error) {
	result := &ZeoUsageReconciliation{Profile: config.ZeoNexusProfile, Credentials: []ZeoUsageBalance{}, Grants: []ZeoUsageBalance{}}
	if err := DB.Model(&ZeoCredential{}).Where("profile = ?", config.ZeoNexusProfile).
		Select("external_id, used_tokens, reserved_tokens").Order("external_id asc").Scan(&result.Credentials).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&ZeoGrant{}).Where("profile = ?", config.ZeoNexusProfile).
		Select("external_id, used_tokens, reserved_tokens").Order("external_id asc").Scan(&result.Grants).Error; err != nil {
		return nil, err
	}
	if err := DB.Model(&ZeoUsage{}).Where("profile = ? AND status <> ?", config.ZeoNexusProfile, ZeoUsagePending).
		Select("COALESCE(MAX(id), 0)").Scan(&result.MaxUsageId).Error; err != nil {
		return nil, err
	}
	return result, nil
}

func StoreZeoNonce(nonce string, createdAt int64) error {
	if len(nonce) < 16 || len(nonce) > 80 {
		return errors.New("invalid nonce")
	}
	_ = DB.Where("created_at < ?", time.Now().Add(-2*config.ZeoNexusSignatureTTL).Unix()).Delete(&ZeoNonce{}).Error
	return DB.Create(&ZeoNonce{Nonce: nonce, CreatedAt: createdAt}).Error
}

func UpsertZeoChannel(externalId string, revision uint, profile, siteId, nodeId, endpointId string, maxConcurrency uint, channel *Channel) (*ZeoManagedChannel, error) {
	if externalId == "" || revision == 0 || profile != config.ZeoNexusProfile {
		return nil, errors.New("invalid channel identity or profile")
	}
	if channel.Type <= channeltype.Unknown || channel.Type >= channeltype.Dummy || strings.TrimSpace(channel.Models) == "" {
		return nil, errors.New("invalid channel type or models")
	}
	if channel.Key != "" {
		protected, err := protectZeoSecret(channel.Key)
		if err != nil {
			return nil, err
		}
		channel.Key = protected
	}
	var managed ZeoManagedChannel
	err := DB.Transaction(func(tx *gorm.DB) error {
		err := tx.Where("external_id = ?", externalId).First(&managed).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if managed.Id != 0 && managed.Revision > revision {
			return nil
		}
		if managed.ChannelId != 0 {
			channel.Id = managed.ChannelId
			var current Channel
			if err := tx.First(&current, managed.ChannelId).Error; err != nil {
				return err
			}
			if channel.Key == "" {
				channel.Key = current.Key
			}
			if err := tx.Model(&Channel{}).Where("id = ?", managed.ChannelId).Save(channel).Error; err != nil {
				return err
			}
			if err := tx.Where("channel_id = ?", managed.ChannelId).Delete(&Ability{}).Error; err != nil {
				return err
			}
			if err := addAbilitiesWithTx(tx, channel); err != nil {
				return err
			}
		} else {
			channel.CreatedTime = helper.GetTimestamp()
			if err := tx.Create(channel).Error; err != nil {
				return err
			}
			if err := addAbilitiesWithTx(tx, channel); err != nil {
				return err
			}
			managed.ChannelId = channel.Id
		}
		managed.ExternalId, managed.Profile, managed.SiteId = externalId, profile, siteId
		managed.NodeId, managed.EndpointId, managed.MaxConcurrency = nodeId, endpointId, maxConcurrency
		managed.Revision, managed.UpdatedAt = revision, time.Now().Unix()
		return tx.Save(&managed).Error
	})
	if err == nil && config.MemoryCacheEnabled {
		InitChannelCache()
	}
	return &managed, err
}

func DisableZeoChannel(externalId string, revision uint) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var managed ZeoManagedChannel
		if err := tx.Where("external_id = ?", externalId).First(&managed).Error; err != nil {
			return err
		}
		if managed.Revision > revision {
			return nil
		}
		managed.Revision, managed.UpdatedAt = revision, time.Now().Unix()
		if err := tx.Save(&managed).Error; err != nil {
			return err
		}
		if err := tx.Model(&Channel{}).Where("id = ?", managed.ChannelId).Update("status", ChannelStatusManuallyDisabled).Error; err != nil {
			return err
		}
		return tx.Model(&Ability{}).Where("channel_id = ?", managed.ChannelId).Update("enabled", false).Error
	})
}

func UpsertZeoRoute(route ZeoRoute) (*ZeoRoute, error) {
	if route.ExternalId == "" || route.Model == "" || route.Revision == 0 || route.Profile != config.ZeoNexusProfile {
		return nil, errors.New("invalid route identity, model or profile")
	}
	if route.Strategy == "" {
		route.Strategy = "priority"
	}
	if route.Mode == "" {
		route.Mode = "chat"
	}
	if route.Mode != "chat" && route.Mode != "embedding" && route.Mode != "rerank" {
		return nil, errors.New("invalid route mode")
	}
	if route.Strategy != "priority" {
		return nil, errors.New("unsupported route strategy")
	}
	if route.Status == "" {
		route.Status = ZeoStatusActive
	}
	if route.Status != ZeoStatusActive && route.Status != ZeoStatusDisabled {
		return nil, errors.New("invalid route status")
	}
	channelRefs := splitCSV(route.ChannelIds)
	if route.Status == ZeoStatusActive && len(channelRefs) == 0 {
		return nil, errors.New("active route has no channels")
	}
	for _, reference := range channelRefs {
		var count int64
		err := DB.Table("zeo_managed_channels z").Joins("JOIN abilities a ON a.channel_id = z.channel_id").
			Where("z.external_id = ? AND z.profile = ? AND a.model = ?", reference, route.Profile, route.Model).Count(&count).Error
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, fmt.Errorf("channel %q is unavailable for model %q", reference, route.Model)
		}
	}
	var current ZeoRoute
	err := DB.Where("external_id = ?", route.ExternalId).First(&current).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if current.Id != 0 && current.Revision > route.Revision {
		return &current, nil
	}
	route.Id, route.UpdatedAt = current.Id, time.Now().Unix()
	return &route, DB.Save(&route).Error
}

func GetZeoRouteMode(modelName, profile string) (string, error) {
	var route ZeoRoute
	err := DB.Where("model = ? AND profile = ? AND status = ?", modelName, profile, ZeoStatusActive).First(&route).Error
	if err != nil {
		return "", err
	}
	if route.Mode == "" {
		return "chat", nil
	}
	return route.Mode, nil
}

func addAbilitiesWithTx(tx *gorm.DB, channel *Channel) error {
	abilities := make([]Ability, 0)
	for _, modelName := range splitCSV(channel.Models) {
		abilities = append(abilities, Ability{Group: channel.Group, Model: modelName, ChannelId: channel.Id,
			Enabled: channel.Status == ChannelStatusEnabled, Priority: channel.Priority})
	}
	if len(abilities) == 0 {
		return errors.New("channel has no models")
	}
	return tx.Create(&abilities).Error
}
