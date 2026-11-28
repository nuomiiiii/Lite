package accounts

import (
	"errors"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
)

const (
	DefaultSessionTTLSeconds = 86400
	MinSessionTTLSeconds     = 60
	MaxSessionTTLSeconds     = 30 * 24 * 60 * 60
)

var ErrSessionExpired = errors.New("session expired")

func NormalizeSessionTTLSeconds(value int) int {
	if value < MinSessionTTLSeconds || value > MaxSessionTTLSeconds {
		return DefaultSessionTTLSeconds
	}
	return value
}

func SessionTTLSeconds() int {
	seconds, err := config.GetAs[int](config.SessionTTLSecondsKey, DefaultSessionTTLSeconds)
	if err != nil {
		return DefaultSessionTTLSeconds
	}
	return NormalizeSessionTTLSeconds(seconds)
}

func SessionTTL() time.Duration {
	return time.Duration(SessionTTLSeconds()) * time.Second
}

func sessionLastActivity(session models.Session) time.Time {
	last := session.LatestOnline
	if last.IsZero() || last.Unix() <= 0 || last.Year() < 2000 {
		last = session.CreatedAt
	}
	if last.IsZero() || last.Unix() <= 0 || last.Year() < 2000 {
		return time.Time{}
	}
	return last
}

func sessionInactive(session models.Session, now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		ttl = time.Duration(DefaultSessionTTLSeconds) * time.Second
	}
	if !session.Expires.After(now) {
		return true
	}
	last := sessionLastActivity(session)
	if last.IsZero() {
		return false
	}
	return !last.Add(ttl).After(now)
}

func cappedSessionExpires(session models.Session, now time.Time, ttl time.Duration) time.Time {
	if ttl <= 0 {
		ttl = time.Duration(DefaultSessionTTLSeconds) * time.Second
	}
	next := session.Expires
	capAt := now.Add(ttl)
	if next.After(capAt) {
		next = capAt
	}
	last := sessionLastActivity(session)
	if !last.IsZero() {
		idleCap := last.Add(ttl)
		if next.After(idleCap) {
			next = idleCap
		}
	}
	return next
}

func SessionForDisplay(session models.Session, now time.Time, ttl time.Duration) models.Session {
	last := sessionLastActivity(session)
	if !last.IsZero() {
		session.LatestOnline = last
	}
	session.Expires = cappedSessionExpires(session, now, ttl)
	return session
}

func TouchSession(plain, userAgent, ip string) (expires time.Time, ttlSeconds int, err error) {
	ttlSeconds = SessionTTLSeconds()
	ttl := time.Duration(ttlSeconds) * time.Second
	now := time.Now().UTC()
	record, lookupErr := lookupSession(plain)
	if lookupErr != nil || sessionInactive(record, now, ttl) {
		return time.Time{}, 0, ErrSessionExpired
	}
	expires = now.Add(ttl)
	hashed, hashErr := hashSessionToken(plain)
	db := dbcore.GetDBInstance()
	query := db.Model(&models.Session{}).Where("session = ?", record.Session)
	if hashErr == nil && hashed != plain {
		query = db.Model(&models.Session{}).Where("session = ? OR session = ?", hashed, plain)
	}
	result := query.Updates(map[string]any{
		"expires":           expires,
		"latest_online":     now,
		"latest_user_agent": userAgent,
		"latest_ip":         ip,
	})
	if result.Error != nil {
		return time.Time{}, 0, result.Error
	}
	if result.RowsAffected == 0 {
		return time.Time{}, 0, ErrSessionExpired
	}
	invalidateSessionCache(plain)
	return expires, ttlSeconds, nil
}

func CapLegacySessionExpires(ttl time.Duration) error {
	now := time.Now().UTC()
	db := dbcore.GetDBInstance()
	var rows []models.Session
	if err := db.Find(&rows).Error; err != nil {
		return err
	}
	for i := range rows {
		next := cappedSessionExpires(rows[i], now, ttl)
		if rows[i].Expires.Equal(next) {
			continue
		}
		if err := db.Model(&models.Session{}).Where("session = ?", rows[i].Session).Update("expires", next).Error; err != nil {
			return err
		}
	}
	return nil
}
