package validation

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

func PlainText(value, field string, maxRunes int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New(field + " cannot be empty")
	}
	if !utf8.ValidString(value) {
		return "", errors.New(field + " must be valid UTF-8")
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return "", errors.New(field + " is too long")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", errors.New(field + " cannot contain control characters")
		}
	}
	return value, nil
}

func Username(value string, minRunes, maxRunes int) (string, error) {
	value = strings.TrimSpace(value)
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z':
			normalized.WriteRune(r + ('a' - 'A'))
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			normalized.WriteRune(r)
		case unicode.Is(unicode.Cf, r):
			// Ignore invisible Unicode formatting marks commonly introduced by
			// clipboard managers, password managers, and browser autofill.
		default:
			return "", fmt.Errorf("username must use %d-%d lowercase letters, numbers, or underscores", minRunes, maxRunes)
		}
	}
	value = normalized.String()
	if len(value) < minRunes || len(value) > maxRunes || !usernamePattern.MatchString(value) {
		return "", fmt.Errorf("username must use %d-%d lowercase letters, numbers, or underscores", minRunes, maxRunes)
	}
	return value, nil
}

func DisplayName(value string, minRunes, maxRunes int) (string, error) {
	value, err := PlainText(value, "display name", maxRunes)
	if err != nil {
		return "", err
	}
	if utf8.RuneCountInString(value) < minRunes {
		return "", errors.New("display name is too short")
	}
	return value, nil
}
