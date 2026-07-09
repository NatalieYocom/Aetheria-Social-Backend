package dbx

import (
	"fmt"
	"strings"
	"unicode"
)

type TextArray []string

func (a *TextArray) Scan(src any) error {
	switch value := src.(type) {
	case nil:
		*a = nil
		return nil
	case string:
		return a.scanString(value)
	case []byte:
		return a.scanString(string(value))
	case []string:
		*a = append((*a)[:0], value...)
		return nil
	default:
		return fmt.Errorf("unsupported text[] value %T", src)
	}
}

func (a *TextArray) scanString(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || value == "{}" {
		*a = []string{}
		return nil
	}
	if !strings.HasPrefix(value, "{") || !strings.HasSuffix(value, "}") {
		*a = []string{value}
		return nil
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(value, "{"), "}")
	if inner == "" {
		*a = []string{}
		return nil
	}

	items := make([]string, 0)
	var b strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range inner {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			items = append(items, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	items = append(items, b.String())
	*a = items
	return nil
}

func PostgresTextArray(values []string) string {
	if len(values) == 0 {
		return "{}"
	}
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		if isSimpleArrayValue(value) {
			encoded = append(encoded, value)
			continue
		}
		value = strings.ReplaceAll(value, `\`, `\\`)
		value = strings.ReplaceAll(value, `"`, `\"`)
		encoded = append(encoded, `"`+value+`"`)
	}
	return "{" + strings.Join(encoded, ",") + "}"
}

func isSimpleArrayValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}
