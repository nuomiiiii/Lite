package passkey

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
	"github.com/nuomiiiii/lite/database/accounts"
	"github.com/nuomiiiii/lite/database/dbcore"
	"github.com/nuomiiiii/lite/database/models"
	"github.com/nuomiiiii/lite/pkg/config"
	"github.com/nuomiiiii/lite/utils"
)

const (
	ceremonyTTL      = 5 * time.Minute
	ceremonyMaxItems = 256
)

var (
	ErrCeremonyNotFound = errors.New("passkey ceremony not found")
	ErrLastLoginMethod  = errors.New("cannot remove the last available sign-in method")
	ErrPasskeyNotFound  = errors.New("passkey not found")
	ErrPasskeyName      = errors.New("passkey name is required")
)

type CeremonyKind string

const (
	CeremonyRegister CeremonyKind = "register"
	CeremonyLogin    CeremonyKind = "login"
)

type Ceremony struct {
	ID       string
	Kind     CeremonyKind
	UserUUID string
	Name     string
	Session  webauthn.SessionData
	Expires  time.Time
}

type ceremonyStore struct {
	mu    sync.Mutex
	items map[string]Ceremony
}

var ceremonies = ceremonyStore{items: map[string]Ceremony{}}

func PutCeremony(kind CeremonyKind, userUUID, name string, session *webauthn.SessionData) string {
	id := uuid.NewString()
	ceremonies.mu.Lock()
	defer ceremonies.mu.Unlock()
	now := time.Now()
	pruneCeremoniesLocked(now)
	for len(ceremonies.items) >= ceremonyMaxItems {
		evictOldestCeremonyLocked()
	}
	ceremonies.items[id] = Ceremony{
		ID:       id,
		Kind:     kind,
		UserUUID: userUUID,
		Name:     name,
		Session:  *session,
		Expires:  now.Add(ceremonyTTL),
	}
	return id
}

func pruneCeremoniesLocked(now time.Time) {
	for key, item := range ceremonies.items {
		if item.Expires.Before(now) {
			delete(ceremonies.items, key)
		}
	}
}

func evictOldestCeremonyLocked() {
	var oldestKey string
	var oldest time.Time
	for key, item := range ceremonies.items {
		if oldestKey == "" || item.Expires.Before(oldest) {
			oldestKey = key
			oldest = item.Expires
		}
	}
	delete(ceremonies.items, oldestKey)
}

func ResetCeremoniesForTest() {
	ceremonies.mu.Lock()
	ceremonies.items = map[string]Ceremony{}
	ceremonies.mu.Unlock()
}

func CeremonyCountForTest() int {
	ceremonies.mu.Lock()
	defer ceremonies.mu.Unlock()
	return len(ceremonies.items)
}

func TakeCeremony(id string, kind CeremonyKind) (Ceremony, error) {
	ceremonies.mu.Lock()
	defer ceremonies.mu.Unlock()
	item, ok := ceremonies.items[id]
	if !ok || item.Kind != kind || item.Expires.Before(time.Now()) {
		delete(ceremonies.items, id)
		return Ceremony{}, ErrCeremonyNotFound
	}
	delete(ceremonies.items, id)
	return item, nil
}

func NewWebAuthn(c *gin.Context) (*webauthn.WebAuthn, error) {
	host := c.Request.Host
	port := ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		host = h
		port = p
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		host = "localhost"
	}
	sitename, _ := config.GetAs[string](config.SitenameKey, "Lite")
	if strings.TrimSpace(sitename) == "" {
		sitename = "Lite"
	}
	scheme := utils.GetScheme(c)
	originHost := c.Request.Host
	origins := []string{scheme + "://" + originHost}
	if host == "localhost" && port != "" {
		origins = append(origins,
			scheme+"://localhost:"+port,
			scheme+"://127.0.0.1:"+port,
		)
	}
	return webauthn.New(&webauthn.Config{
		RPDisplayName:         sitename,
		RPID:                  host,
		RPOrigins:             uniqueStrings(origins),
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func UserHandle(userUUID string) []byte {
	parsed, err := uuid.Parse(userUUID)
	if err != nil {
		return []byte(userUUID)
	}
	return parsed[:]
}

func UserUUIDFromHandle(handle []byte) string {
	if len(handle) == 16 {
		var parsed uuid.UUID
		copy(parsed[:], handle)
		return parsed.String()
	}
	return string(handle)
}

func EncodeCredentialID(id []byte) string {
	return base64.RawURLEncoding.EncodeToString(id)
}

type accountUser struct {
	user  models.User
	creds []webauthn.Credential
}

func (u accountUser) WebAuthnID() []byte {
	return UserHandle(u.user.UUID)
}

func (u accountUser) WebAuthnName() string {
	return u.user.Username
}

func (u accountUser) WebAuthnDisplayName() string {
	return u.user.Username
}

func (u accountUser) WebAuthnCredentials() []webauthn.Credential {
	return u.creds
}

func LoadUser(userUUID string) (webauthn.User, error) {
	user, err := accounts.GetUserByUUID(userUUID)
	if err != nil {
		return nil, err
	}
	rows, err := ListCredentials(userUUID)
	if err != nil {
		return nil, err
	}
	creds := make([]webauthn.Credential, 0, len(rows))
	for _, row := range rows {
		var cred webauthn.Credential
		if err := json.Unmarshal(row.CredentialData, &cred); err != nil {
			continue
		}
		creds = append(creds, cred)
	}
	return accountUser{user: user, creds: creds}, nil
}

func LoadUserByHandle(rawID, userHandle []byte) (webauthn.User, error) {
	if len(userHandle) > 0 {
		return LoadUser(UserUUIDFromHandle(userHandle))
	}
	if len(rawID) == 0 {
		return nil, ErrPasskeyNotFound
	}
	var row models.PasskeyCredential
	err := dbcore.GetDBInstance().Where("credential_id = ?", EncodeCredentialID(rawID)).First(&row).Error
	if err != nil {
		return nil, ErrPasskeyNotFound
	}
	return LoadUser(row.UserUUID)
}

type Summary struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	AAGUID     string     `json:"aaguid,omitempty"`
}

func ListCredentials(userUUID string) ([]models.PasskeyCredential, error) {
	var rows []models.PasskeyCredential
	err := dbcore.GetDBInstance().Where("user_uuid = ?", userUUID).Order("created_at asc").Find(&rows).Error
	return rows, err
}

func Summaries(userUUID string) ([]Summary, error) {
	rows, err := ListCredentials(userUUID)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(rows))
	for _, row := range rows {
		out = append(out, Summary{
			ID:         row.ID,
			Name:       row.Name,
			CreatedAt:  row.CreatedAt,
			LastUsedAt: row.LastUsedAt,
			AAGUID:     row.AAGUID,
		})
	}
	return out, nil
}

func SaveCredential(userUUID, rpID, name, userAgent string, cred *webauthn.Credential) (Summary, error) {
	payload, err := json.Marshal(cred)
	if err != nil {
		return Summary{}, err
	}
	row := models.PasskeyCredential{
		ID:                uuid.NewString(),
		UserUUID:          userUUID,
		RPID:              rpID,
		CredentialID:      EncodeCredentialID(cred.ID),
		Name:              name,
		CredentialData:    payload,
		AAGUID:            encodeAAGUID(cred.Authenticator.AAGUID),
		CreatedAt:         time.Now().UTC(),
		CreationUserAgent: userAgent,
	}
	if err := dbcore.GetDBInstance().Create(&row).Error; err != nil {
		return Summary{}, err
	}
	return Summary{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt, AAGUID: row.AAGUID}, nil
}

func UpdateCredential(userUUID string, cred *webauthn.Credential) error {
	payload, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return dbcore.GetDBInstance().Model(&models.PasskeyCredential{}).
		Where("user_uuid = ? AND credential_id = ?", userUUID, EncodeCredentialID(cred.ID)).
		Updates(map[string]any{
			"credential_data": payload,
			"last_used_at":    now,
		}).Error
}

func RenameCredential(userUUID, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrPasskeyName
	}
	result := dbcore.GetDBInstance().Model(&models.PasskeyCredential{}).
		Where("id = ? AND user_uuid = ?", id, userUUID).
		Update("name", name)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrPasskeyNotFound
	}
	return nil
}

func DeleteCredential(userUUID, id string) error {
	var row models.PasskeyCredential
	db := dbcore.GetDBInstance()
	if err := db.Where("id = ? AND user_uuid = ?", id, userUUID).First(&row).Error; err != nil {
		return ErrPasskeyNotFound
	}
	ok, err := canRemovePasskey(userUUID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLastLoginMethod
	}
	return db.Delete(&row).Error
}

// SignInAvailability is the shared password, site sign-in, and passkey check.
// A method counts only when it can actually be used to sign in.
type SignInAvailability struct {
	PasswordDisabled bool
	HasPassword      bool
	OAuthEnabled     bool
	SSOBound         bool
	PasskeyCount     int
}

func (a SignInAvailability) Remaining() int {
	count := 0
	if !a.PasswordDisabled && a.HasPassword {
		count++
	}
	if a.OAuthEnabled && a.SSOBound {
		count++
	}
	if a.PasskeyCount > 0 {
		count++
	}
	return count
}

func CurrentSignInAvailability(userUUID string) (SignInAvailability, error) {
	user, err := accounts.GetUserByUUID(userUUID)
	if err != nil {
		return SignInAvailability{}, err
	}
	var count int64
	if err := dbcore.GetDBInstance().Model(&models.PasskeyCredential{}).Where("user_uuid = ?", userUUID).Count(&count).Error; err != nil {
		return SignInAvailability{}, err
	}
	passwordDisabled, _ := config.GetAs[bool](config.DisablePasswordLoginKey, false)
	oauthEnabled, _ := config.GetAs[bool](config.OAuthEnabledKey, false)
	return SignInAvailability{
		PasswordDisabled: passwordDisabled,
		HasPassword:      strings.TrimSpace(user.Passwd) != "",
		OAuthEnabled:     oauthEnabled,
		SSOBound:         strings.TrimSpace(user.SSOID) != "",
		PasskeyCount:     int(count),
	}, nil
}

func canRemovePasskey(userUUID string) (bool, error) {
	avail, err := CurrentSignInAvailability(userUUID)
	if err != nil {
		return false, err
	}
	if avail.PasskeyCount <= 0 {
		return false, nil
	}
	avail.PasskeyCount--
	return avail.Remaining() > 0, nil
}

func encodeAAGUID(raw []byte) string {
	if len(raw) != 16 {
		return ""
	}
	var id uuid.UUID
	copy(id[:], raw)
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}
