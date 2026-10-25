package mcp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nuomiiiii/lite/pkg/config"
)

const (
	MinDurationMinutes        = 1
	DefaultMaxDurationMinutes = 24 * 60
	HardMaxDurationMinutes    = 72 * 60
	DefaultDurationMinutes    = 30
	DefaultMaxConcurrency     = 4
	MaxConcurrencyCap         = 16
	DefaultAccessTokenTTL     = 5 * time.Minute
	MinAccessTokenTTLMinutes  = 1
	MaxAccessTokenTTLMinutes  = 24 * 60
	AuthCodeTTL               = 180 * time.Second
	DefaultExecTimeout        = 5 * time.Minute
	MaxOutputCache            = 1 << 20
	AdminOutputPreviewMax     = 800
	DefaultReadBytes          = 64 << 10
	MaxReadBytes              = 256 << 10
	PolicyVersion             = 1
	MaxActiveLeases           = 32
	MaxActiveLeasesPerUser    = 8
)

var (
	ErrDurationInvalid = errors.New("authorization duration must be an integer between 1 and 4320 minutes")
	ErrDurationOverMax = errors.New("authorization duration exceeds the site maximum")
	ErrSettingsInvalid = errors.New("MCP duration settings are invalid")
)

type DurationSettings struct {
	DefaultMinutes int
	MaxMinutes     int
	MaxConcurrency int
}

func siteDurationSettings() DurationSettings {
	defaults := DurationSettings{
		DefaultMinutes: DefaultDurationMinutes,
		MaxMinutes:     DefaultMaxDurationMinutes,
		MaxConcurrency: DefaultMaxConcurrency,
	}
	if v, err := config.GetAs[int](config.MCPDefaultDurationMinKey, DefaultDurationMinutes); err == nil {
		defaults.DefaultMinutes = v
	}
	if v, err := config.GetAs[int](config.MCPMaxDurationMinKey, DefaultMaxDurationMinutes); err == nil {
		defaults.MaxMinutes = v
	}
	if v, err := config.GetAs[int](config.MCPMaxConcurrencyKey, DefaultMaxConcurrency); err == nil {
		defaults.MaxConcurrency = v
	}
	normalized, err := NormalizeSettings(defaults.DefaultMinutes, defaults.MaxMinutes, defaults.MaxConcurrency)
	if err != nil {
		return DurationSettings{
			DefaultMinutes: DefaultDurationMinutes,
			MaxMinutes:     DefaultMaxDurationMinutes,
			MaxConcurrency: DefaultMaxConcurrency,
		}
	}
	return normalized
}

// accessTokenTTLMinutes clamps a configured access-token lifetime into the range
// this server accepts, so a bad setting cannot mint a token that outlives the
// authorization it belongs to.
func accessTokenTTLMinutes(minutes int) int {
	if minutes < MinAccessTokenTTLMinutes {
		return MinAccessTokenTTLMinutes
	}
	if minutes > MaxAccessTokenTTLMinutes {
		return MaxAccessTokenTTLMinutes
	}
	return minutes
}

// AccessTokenLifetime is the lifetime of a freshly issued MCP access token.
//
// Short by default, because every MCP request re-checks the live authorization.
// The setting exists for deployments whose clients each refresh on their own
// schedule: a longer lifetime means fewer refresh round trips, not a longer
// grant. It can never outlive the authorization itself, because
// issueTokenPairInDB still truncates it to the lease expiry.
func AccessTokenLifetime() time.Duration {
	minutes := int(DefaultAccessTokenTTL / time.Minute)
	if v, err := config.GetAs[int](config.MCPAccessTokenTTLMinKey, minutes); err == nil {
		minutes = v
	}
	return time.Duration(accessTokenTTLMinutes(minutes)) * time.Minute
}

func NormalizeSettings(defaultMinutes, maxMinutes, concurrency int) (DurationSettings, error) {
	if maxMinutes < MinDurationMinutes || maxMinutes > HardMaxDurationMinutes {
		return DurationSettings{}, ErrSettingsInvalid
	}
	if defaultMinutes < MinDurationMinutes || defaultMinutes > maxMinutes {
		return DurationSettings{}, ErrSettingsInvalid
	}
	if concurrency < 1 || concurrency > MaxConcurrencyCap {
		return DurationSettings{}, ErrSettingsInvalid
	}
	return DurationSettings{
		DefaultMinutes: defaultMinutes,
		MaxMinutes:     maxMinutes,
		MaxConcurrency: concurrency,
	}, nil
}

func ParseDurationMinutes(raw any) (int, error) {
	switch value := raw.(type) {
	case nil:
		return 0, ErrDurationInvalid
	case int:
		return normalizeDurationMinutes(value)
	case int32:
		return normalizeDurationMinutes(int(value))
	case int64:
		return normalizeDurationMinutes(int(value))
	case float64:
		if value != float64(int(value)) {
			return 0, ErrDurationInvalid
		}
		return normalizeDurationMinutes(int(value))
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return 0, ErrDurationMinutesInvalid(trimmed)
		}
		if strings.ContainsAny(trimmed, ".eE") {
			return 0, ErrDurationInvalid
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return 0, ErrDurationInvalid
		}
		return normalizeDurationMinutes(parsed)
	default:
		return 0, ErrDurationInvalid
	}
}

func ErrDurationMinutesInvalid(raw string) error {
	if raw == "" {
		return ErrDurationInvalid
	}
	return ErrDurationInvalid
}

func normalizeDurationMinutes(value int) (int, error) {
	if value < MinDurationMinutes || value > HardMaxDurationMinutes {
		return 0, ErrDurationInvalid
	}
	return value, nil
}

func ValidateLeaseDuration(minutes, siteMax int) (int, error) {
	normalized, err := normalizeDurationMinutes(minutes)
	if err != nil {
		return 0, err
	}
	if siteMax < MinDurationMinutes || siteMax > HardMaxDurationMinutes {
		siteMax = HardMaxDurationMinutes
	}
	if normalized > siteMax {
		return 0, fmt.Errorf("%w (%d)", ErrDurationOverMax, siteMax)
	}
	return normalized, nil
}

func leaseExpiresAt(issuedAt time.Time, minutes int) time.Time {
	return issuedAt.UTC().Add(time.Duration(minutes) * time.Minute)
}

func truncateTTL(now, expiresAt time.Time, ttl time.Duration) time.Duration {
	if !expiresAt.After(now) {
		return 0
	}
	remain := expiresAt.Sub(now)
	if remain < ttl {
		return remain
	}
	return ttl
}

func mcpEnabled() bool {
	enabled, err := config.GetAs[bool](config.AllowMCPKey, false)
	return err == nil && enabled
}

var isMCPEnabled = mcpEnabled
