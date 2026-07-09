package dbx

import "testing"

func TestTextArrayScansPostgresLiteral(t *testing.T) {
	var value TextArray
	if err := value.Scan(`{smoke,"basis world"}`); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(value) != 2 || value[0] != "smoke" || value[1] != "basis world" {
		t.Fatalf("value = %#v", value)
	}
}

func TestPostgresTextArrayEncodesSimpleAndQuotedValues(t *testing.T) {
	got := PostgresTextArray([]string{"smoke", "basis world"})
	if got != `{smoke,"basis world"}` {
		t.Fatalf("PostgresTextArray = %q", got)
	}
}
