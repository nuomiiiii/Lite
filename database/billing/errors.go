package billing

import (
	"errors"
	"fmt"
)

var ErrInvalidInput = errors.New("invalid billing input")

func invalidInputf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidInput, fmt.Sprintf(format, args...))
}
