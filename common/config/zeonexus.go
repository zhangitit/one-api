package config

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var ZeoNexusEnabled = strings.EqualFold(os.Getenv("ZEO_NEXUS_ENABLED"), "true")
var ZeoNexusProfile = strings.ToLower(strings.TrimSpace(os.Getenv("ZEO_NEXUS_PROFILE")))
var ZeoNexusControlSecret = os.Getenv("ZEO_NEXUS_CONTROL_SECRET")
var ZeoNexusMasterKey = os.Getenv("ZEO_NEXUS_MASTER_KEY")
var ZeoNexusSignatureTTL = envDuration("ZEO_NEXUS_SIGNATURE_TTL_SECONDS", 300) * time.Second
var ZeoNexusDefaultReserveTokens = envInt64("ZEO_NEXUS_DEFAULT_RESERVE_TOKENS", 8192)
var ZeoNexusMaxReserveTokens = envInt64("ZEO_NEXUS_MAX_RESERVE_TOKENS", 262144)
var ZeoNexusMaxRequestBodyBytes = envInt64("ZEO_NEXUS_MAX_REQUEST_BODY_BYTES", 4<<20)
var ZeoNexusAllowedUpstreamHosts = splitEnv("ZEO_NEXUS_ALLOWED_UPSTREAM_HOSTS")
var ZeoNexusAllowInsecureHTTP = strings.EqualFold(os.Getenv("ZEO_NEXUS_ALLOW_INSECURE_HTTP"), "true")

func init() {
	if ZeoNexusProfile == "" {
		ZeoNexusProfile = "aggregation"
	}
}

func ValidateZeoNexusUpstreamURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("upstream base URL is invalid")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && ZeoNexusAllowInsecureHTTP) {
		return fmt.Errorf("upstream base URL must use HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	allowed := false
	for _, candidate := range ZeoNexusAllowedUpstreamHosts {
		if strings.EqualFold(candidate, host) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("upstream host %q is not allowlisted", host)
	}
	if ip := net.ParseIP(host); ip != nil && ZeoNexusProfile != "inference" && isPrivateZeoIP(ip) {
		return fmt.Errorf("private upstream addresses are only allowed by the inference gateway")
	}
	if ZeoNexusProfile == "aggregation" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
		if lookupErr != nil || len(addresses) == 0 {
			return fmt.Errorf("upstream host %q cannot be resolved", host)
		}
		for _, address := range addresses {
			if isPrivateZeoIP(address.IP) {
				return fmt.Errorf("upstream host %q resolves to a restricted address", host)
			}
		}
	}
	return nil
}

func isPrivateZeoIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

func splitEnv(name string) []string {
	result := make([]string, 0)
	for _, item := range strings.Split(os.Getenv(name), ",") {
		if item = strings.ToLower(strings.TrimSpace(item)); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func ValidateZeoNexus() error {
	if !ZeoNexusEnabled {
		return nil
	}
	if ZeoNexusProfile != "aggregation" && ZeoNexusProfile != "inference" {
		return fmt.Errorf("ZEO_NEXUS_PROFILE must be aggregation or inference")
	}
	if len(ZeoNexusControlSecret) < 32 {
		return fmt.Errorf("ZEO_NEXUS_CONTROL_SECRET must contain at least 32 characters")
	}
	if len(ZeoNexusMasterKey) < 32 {
		return fmt.Errorf("ZEO_NEXUS_MASTER_KEY must contain at least 32 characters")
	}
	return nil
}

func envDuration(name string, fallback int64) time.Duration {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || value <= 0 {
		value = fallback
	}
	return time.Duration(value)
}

func envInt64(name string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
