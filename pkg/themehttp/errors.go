package themehttp

import (
	"context"
	"errors"
	"net"
	"net/url"
)

var (
	ErrInvalidURL        = errors.New("invalid URL")
	ErrUnsupportedScheme = errors.New("unsupported URL scheme")
	ErrPrivateAddress    = errors.New("destination is a private or reserved address")
	ErrDNS               = errors.New("DNS lookup failed")
	ErrRedirect          = errors.New("redirect was rejected")
	ErrTooManyRedirects  = errors.New("too many redirects")
	ErrTimeout           = errors.New("connection timed out")
	ErrHTTPStatus        = errors.New("unexpected HTTP status")
	ErrEmpty             = errors.New("empty response")
	ErrTooLarge          = errors.New("response exceeds the size limit")
	ErrTempFile          = errors.New("failed to write temporary file")
)

func mapRequestError(err error) error {
	if err == nil {
		return nil
	}
	if mapped := knownError(err); mapped != nil {
		return mapped
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return ErrTimeout
		}
		if mapped := knownError(ue.Err); mapped != nil {
			return mapped
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	return err
}

func knownError(err error) error {
	switch {
	case errors.Is(err, ErrInvalidURL):
		return ErrInvalidURL
	case errors.Is(err, ErrUnsupportedScheme):
		return ErrUnsupportedScheme
	case errors.Is(err, ErrPrivateAddress):
		return ErrPrivateAddress
	case errors.Is(err, ErrDNS):
		return ErrDNS
	case errors.Is(err, ErrRedirect):
		return ErrRedirect
	case errors.Is(err, ErrTooManyRedirects):
		return ErrTooManyRedirects
	case errors.Is(err, ErrTimeout):
		return ErrTimeout
	case errors.Is(err, ErrHTTPStatus):
		return err
	case errors.Is(err, ErrEmpty):
		return ErrEmpty
	case errors.Is(err, ErrTooLarge):
		return ErrTooLarge
	case errors.Is(err, ErrTempFile):
		return ErrTempFile
	default:
		return nil
	}
}
