package ws

import (
	"net/http"
	"net/url"
)

func CheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	host := r.Host
	originUrl, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return originUrl.Host == host
}
