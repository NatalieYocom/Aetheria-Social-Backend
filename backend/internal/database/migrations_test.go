package database

import "testing"

func TestSplitSQLStatementsKeepsDollarQuotedFunctionBody(t *testing.T) {
	input := `
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users (
  id UUID PRIMARY KEY
);
`

	statements := splitSQLStatements(input)

	if len(statements) != 2 {
		t.Fatalf("expected 2 statements, got %d: %#v", len(statements), statements)
	}
	if statements[0] == "" || statements[1] == "" {
		t.Fatalf("statements should not be empty: %#v", statements)
	}
}
