package accounts

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	messageevent "github.com/nuomiiiii/lite/database/models/messageEvent"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils"
	"github.com/nuomiiiii/lite/utils/geoip"
	"github.com/nuomiiiii/lite/utils/messageSender"
)

func GetAllSessions() (sessions []models.Session, err error) {
	db := dbcore.GetDBInstance()
	err = db.Find(&sessions).Error
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

func CreateSession(uuid string, expires int, userAgent, ip, login_method string) (string, error) {
	db := dbcore.GetDBInstance()
	session := utils.GenerateRandomString(32)
	hashed, err := hashSessionToken(session)
	if err != nil {
		return "", err
	}

	sessionRecord := models.Session{
		UUID:         uuid,
		Session:      hashed,
		Expires:      time.Now().UTC().Add(time.Duration(expires) * time.Second),
		UserAgent:    userAgent,
		Ip:           ip,
		LoginMethod:  login_method,
		LatestOnline: time.Now().UTC(),
	}
	go func() {
		LoginNotification, _ := config.GetAs[bool](config.LoginNotificationKey, false)
		if LoginNotification {
			ipAddr := net.ParseIP(ip)
			ipinfo, _ := geoip.GetGeoInfo(ipAddr)
			loc := "unknown"
			if ipinfo != nil && ipinfo.Name != "" {
				loc = ipinfo.Name
			}
			messageSender.SendEvent(models.EventMessage{
				Event:   messageevent.Login,
				Time:    time.Now().UTC(),
				Message: fmt.Sprintf("%s: %s (%s)\n%s", login_method, ip, loc, userAgent),
				Emoji:   "🔑",
			})
		}
	}()

	if err := db.Create(&sessionRecord).Error; err != nil {
		return "", err
	}
	return session, nil
}

func lookupSession(plain string) (models.Session, error) {
	db := dbcore.GetDBInstance()
	var sessionRecord models.Session
	if hashed, err := hashSessionToken(plain); err == nil {
		if err := db.Where("session = ?", hashed).First(&sessionRecord).Error; err == nil {
			return sessionRecord, nil
		}
	}
	// Hashed database values must not work as cookies. Only leftover
	// pre-migration plaintext tokens may be looked up directly.
	if looksHashedSession(plain) {
		return models.Session{}, errors.New("session not found")
	}
	if err := db.Where("session = ?", plain).First(&sessionRecord).Error; err != nil {
		return models.Session{}, err
	}
	return sessionRecord, nil
}

func SessionStillValid(userUUID, loginSession string) bool {
	if userUUID == "" || loginSession == "" {
		return false
	}
	key := SessionLookupKey(loginSession)
	var found int
	err := dbcore.GetDBInstance().
		Model(&models.Session{}).
		Select("1").
		Joins("INNER JOIN users ON users.uuid = sessions.uuid").
		Where("sessions.session = ? AND sessions.uuid = ? AND sessions.expires > ?", key, userUUID, time.Now().UTC()).
		Limit(1).
		Scan(&found).Error
	return err == nil && found == 1
}

func GetSession(session string) (uuid string, err error) {
	sessionRecord, err := lookupSession(session)
	if err != nil {
		return "", err
	}
	if time.Now().UTC().After(sessionRecord.Expires) {
		_ = DeleteSession(session)
		return "", errors.New("session expired")
	}
	return sessionRecord.UUID, nil
}

func GetUserBySession(session string) (models.User, error) {
	sessionRecord, err := lookupSession(session)
	if err != nil {
		return models.User{}, err
	}
	return GetUserByUUID(sessionRecord.UUID)
}

func DeleteSession(session string) (err error) {
	db := dbcore.GetDBInstance()
	hashed, hashErr := hashSessionToken(session)
	query := db.Where("session = ?", session)
	if hashErr == nil && hashed != session {
		query = db.Where("session = ? OR session = ?", hashed, session)
	}
	result := query.Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func DeleteAllSessions() error {
	return InvalidateAllSessions()
}

func UpdateLatest(session, useragent, ip string) error {
	db := dbcore.GetDBInstance()
	hashed, err := hashSessionToken(session)
	if err != nil {
		return db.Model(&models.Session{}).Where("session = ?", session).Updates(map[string]interface{}{
			"latest_online":     time.Now().UTC(),
			"latest_user_agent": useragent,
			"latest_ip":         ip,
		}).Error
	}
	return db.Model(&models.Session{}).Where("session = ? OR session = ?", hashed, session).Updates(map[string]interface{}{
		"latest_online":     time.Now().UTC(),
		"latest_user_agent": useragent,
		"latest_ip":         ip,
	}).Error
}

func RemoveExpiredSessions() error {
	db := dbcore.GetDBInstance()
	result := db.Where("expires < ?", time.Now().UTC()).Delete(&models.Session{})
	if result.Error != nil {
		return result.Error
	}
	return nil
}

func HashSessionToken(plain string) (string, error) {
	return hashSessionToken(plain)
}

func SessionLookupKey(value string) string {
	if looksHashedSession(value) {
		return value
	}
	hashed, err := hashSessionToken(value)
	if err != nil {
		return value
	}
	return hashed
}
