//go:build integration

package postgres_test

import (
	"os"
	"testing"

	"github.com/schemigo/schemigo/internal/testdb"
)

func TestMain(m *testing.M) {
	os.Exit(testdb.Run(m))
}

func TestPostgresFixtures(t *testing.T) {
	if os.Getenv("SCHEMIGO_TESTCONTAINERS") != "1" {
		t.Skip("disposable fixtures disabled")
	}
	var names, clusters [2]string
	if !t.Run("parallel databases", func(t *testing.T) {
		for i, name := range []string{"first", "second"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				db := testdb.Postgres(t)
				if err := db.QueryRowContext(t.Context(), `SELECT current_database(), system_identifier::text FROM pg_control_system()`).Scan(&names[i], &clusters[i]); err != nil {
					t.Fatal(err)
				}
				execFixture(t, db, `CREATE TABLE public.isolated (value integer); INSERT INTO public.isolated VALUES (42)`)
				var value int
				if err := db.QueryRowContext(t.Context(), `SELECT value FROM public.isolated`).Scan(&value); err != nil || value != 42 {
					t.Fatalf("fixture value=%d, err=%v", value, err)
				}
			})
		}
	}) {
		return
	}
	if names[0] == names[1] || clusters[0] != clusters[1] {
		t.Fatalf("fixtures must use separate databases on one cluster: names=%v clusters=%v", names, clusters)
	}
	db := testdb.Postgres(t)
	for _, name := range names {
		var exists bool
		if err := db.QueryRowContext(t.Context(), `SELECT EXISTS (SELECT FROM pg_database WHERE datname=$1)`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatalf("test database %q survived cleanup", name)
		}
	}
}
