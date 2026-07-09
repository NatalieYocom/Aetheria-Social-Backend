package validate

import (
	"net/mail"
	"regexp"
	"strings"
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{2,31}$`)

func Email(value string) bool {
	_, err := mail.ParseAddress(strings.TrimSpace(value))
	return err == nil
}

func Username(value string) bool {
	return usernamePattern.MatchString(strings.TrimSpace(value))
}

func Required(value string) bool {
	return strings.TrimSpace(value) != ""
}
