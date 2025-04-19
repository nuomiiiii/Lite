package utils

import (
	"encoding/base64"
	"math/rand"
	"time"
)

func GenerateRandomString(length int) string {
	b := make([]byte, length)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	_, err := r.Read(b)
	if err != nil {
		return ""
	}
	return base64.URLEncoding.EncodeToString(b)[:length]
}

func GeneratePassword() string {
	return GenerateRandomString(12)
}

func GenerateToken() string {
	return GenerateRandomString(16)
}