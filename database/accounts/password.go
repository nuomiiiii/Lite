package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	legacyPasswordSalt = "06Wm4Jv1Hkxx"
	argonTime          = 3
	argonMemory        = 32 * 1024
	argonThreads       = 1
	argonKeyLen        = 32
	argonSaltLen       = 16
	argonPrefix        = "$argon2id$"

	argonMaxMemory  = 64 * 1024
	argonMaxTime    = 8
	argonMaxThreads = 4
	argonMaxKeyLen  = 64
)

var (
	ErrPasswordBusy    = errors.New("系统繁忙，请稍后重试")
	ErrPasswordInvalid = errors.New("invalid credentials")

	argon2Slots          = make(chan struct{}, 1)
	argon2VerifyObserver func()
)

func hashPasswd(passwd string) (string, error) {
	if !tryAcquireArgon2() {
		return "", ErrPasswordBusy
	}
	defer releaseArgon2()
	notifyArgon2Observer()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(passwd), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
		argonPrefix,
		argon2.Version,
		argonMemory,
		argonTime,
		argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum),
	), nil
}

func hashLegacySHA256(passwd string) string {
	sum := sha256.Sum256([]byte(passwd + legacyPasswordSalt))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func verifyPasswd(passwd, encoded string) bool {
	if strings.HasPrefix(encoded, argonPrefix) {
		return verifyArgon2id(passwd, encoded)
	}
	legacy := hashLegacySHA256(passwd)
	return subtle.ConstantTimeCompare([]byte(legacy), []byte(encoded)) == 1
}

func verifyPasswordLimited(passwd, encoded string) (bool, error) {
	if strings.HasPrefix(encoded, argonPrefix) {
		return verifyArgon2idLimited(passwd, encoded)
	}
	legacy := hashLegacySHA256(passwd)
	return subtle.ConstantTimeCompare([]byte(legacy), []byte(encoded)) == 1, nil
}

func isLegacyPasswordHash(encoded string) bool {
	return encoded != "" && !strings.HasPrefix(encoded, argonPrefix)
}

func verifyArgon2idLimited(passwd, encoded string) (bool, error) {
	if !tryAcquireArgon2() {
		return false, ErrPasswordBusy
	}
	defer releaseArgon2()
	notifyArgon2Observer()
	return verifyArgon2id(passwd, encoded), nil
}

func verifyArgon2id(passwd, encoded string) bool {
	salt, sum, time, memory, threads, keyLen, ok := parseArgon2id(encoded)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(passwd), salt, time, memory, threads, keyLen)
	return subtle.ConstantTimeCompare(got, sum) == 1
}

func parseArgon2id(encoded string) (salt, hash []byte, time, memory uint32, threads uint8, keyLen uint32, ok bool) {
	// $argon2id$v=19$m=32768,t=3,p=1$salt$hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, 0, false
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return nil, nil, 0, 0, 0, 0, false
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return nil, nil, 0, 0, 0, 0, false
	}
	memory64, err := parsePrefixedUint(params[0], "m=")
	if err != nil || memory64 < 1 || memory64 > argonMaxMemory {
		return nil, nil, 0, 0, 0, 0, false
	}
	time64, err := parsePrefixedUint(params[1], "t=")
	if err != nil || time64 < 1 || time64 > argonMaxTime {
		return nil, nil, 0, 0, 0, 0, false
	}
	threads64, err := parsePrefixedUint(params[2], "p=")
	if err != nil || threads64 < 1 || threads64 > argonMaxThreads {
		return nil, nil, 0, 0, 0, 0, false
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return nil, nil, 0, 0, 0, 0, false
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(hash) == 0 || uint32(len(hash)) > argonMaxKeyLen {
		return nil, nil, 0, 0, 0, 0, false
	}
	return salt, hash, uint32(time64), uint32(memory64), uint8(threads64), uint32(len(hash)), true
}

func parsePrefixedUint(value, prefix string) (uint64, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, fmt.Errorf("missing prefix")
	}
	return strconv.ParseUint(strings.TrimPrefix(value, prefix), 10, 32)
}

func tryAcquireArgon2() bool {
	select {
	case argon2Slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseArgon2() {
	<-argon2Slots
}

func IsPasswordBusy(err error) bool {
	return errors.Is(err, ErrPasswordBusy)
}

func HoldArgon2ForTest() (release func(), ok bool) {
	if !tryAcquireArgon2() {
		return nil, false
	}
	return releaseArgon2, true
}

func notifyArgon2Observer() {
	if argon2VerifyObserver != nil {
		argon2VerifyObserver()
	}
}

func SetArgon2VerifyObserverForTest(fn func()) {
	argon2VerifyObserver = fn
}
