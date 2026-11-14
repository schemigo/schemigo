package mysql

import "testing"

func TestParseURL(t *testing.T) {
	cfg, err := parseURL("mysql://alice:p%40ss@[::1]:3307/app?tls=true&timeout=5s")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "alice" || cfg.Passwd != "p@ss" || cfg.Addr != "[::1]:3307" || cfg.DBName != "app" || cfg.TLSConfig != "true" || !cfg.ParseTime || !cfg.MultiStatements {
		t.Fatalf("unexpected MySQL config: %+v", cfg)
	}
}

func TestParseURLDefaults(t *testing.T) {
	cfg, err := parseURL("mysql://user:secret@localhost/app?multiStatements=false&parseTime=false")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "localhost:3306" || !cfg.ParseTime || !cfg.MultiStatements {
		t.Fatalf("required driver settings were disabled: %+v", cfg)
	}
}

func TestParseURLRejectsInvalidConnection(t *testing.T) {
	for _, raw := range []string{"postgres://user@localhost/app", "mysql://localhost/app", "mysql://user@localhost/", "mysql://user@localhost/app/extra", "mysql://user@localhost/app?timeout=invalid"} {
		if _, err := parseURL(raw); err == nil {
			t.Errorf("accepted invalid URL %q", raw)
		}
	}
}
