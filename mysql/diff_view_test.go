package mysql

import "testing"

func TestSameViewDefinition(t *testing.T) {
	if !sameViewDefinition("select `app`.`users`.`id` AS `id` from `app`.`users`", "SELECT id FROM users", "app") {
		t.Fatal("MySQL formatting caused view drift")
	}
	if sameViewDefinition("SELECT id FROM users", "SELECT id AS changed FROM users", "app") {
		t.Fatal("changed view was treated as unchanged")
	}
	if sameViewDefinition("SELECT 'app.foo' AS value", "SELECT 'foo' AS value", "app") {
		t.Fatal("schema name inside a literal was removed")
	}
	if sameViewDefinition("SELECT 'a b' AS value", "SELECT 'a  b' AS value", "app") {
		t.Fatal("whitespace inside a literal was changed")
	}
	if sameViewDefinition("SELECT a.id FROM a JOIN b ON a.id=b.id", "SELECT b.id FROM a JOIN b ON a.id=b.id", "app") {
		t.Fatal("join qualifiers were ignored")
	}
}
