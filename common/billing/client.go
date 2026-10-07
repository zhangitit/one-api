// Package billing calls the Console's authoritative wallet. No replicated cash balance is trusted.
package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/songquanpeng/one-api/common/config"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Request never follows redirects or environment proxies: a signing secret must stay at the configured origin.
func Request(ctx context.Context, action string, payload any) (map[string]any, error) {
	origin := strings.TrimRight(config.ZeoNexusConsoleControlURL, "/")
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && config.ZeoNexusConsoleAllowHTTP)) {
		return nil, &Error{503, "资金预占服务尚未配置，暂不能调用付费模型"}
	}
	if action != "reserve" && action != "finalize" {
		return nil, errors.New("invalid billing action")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	path := "/billing/gateway/" + action
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonceBytes := make([]byte, 20)
	if _, err = rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := hex.EncodeToString(nonceBytes)
	digest := sha256.Sum256(body)
	canonical := strings.Join([]string{http.MethodPost, path, stamp, nonce, hex.EncodeToString(digest[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(config.ZeoNexusControlSecret))
	_, _ = mac.Write([]byte(canonical))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Zeo-Profile", config.ZeoNexusProfile)
	req.Header.Set("X-Zeo-Timestamp", stamp)
	req.Header.Set("X-Zeo-Nonce", nonce)
	req.Header.Set("X-Zeo-Signature", hex.EncodeToString(mac.Sum(nil)))
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, &Error{503, "资金预占服务连接失败，请稍后重试"}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return nil, &Error{503, "资金预占服务响应无效"}
	}
	var result struct {
		Data  map[string]any `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil, &Error{503, "资金预占服务响应无效"}
	}
	if resp.StatusCode != 200 {
		message := result.Error.Message
		if message == "" {
			message = fmt.Sprintf("资金预占服务返回 %d", resp.StatusCode)
		}
		return nil, &Error{resp.StatusCode, message}
	}
	return result.Data, nil
}
