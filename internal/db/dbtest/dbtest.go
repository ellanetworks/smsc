package dbtest

import (
	"context"
	"testing"

	"github.com/ellanetworks/smsc/internal/db"
)

func Migrate(t testing.TB, d *db.DB) {
	t.Helper()

	for target := d.Schema() + 1; target <= db.SchemaVersion(); target++ {
		if err := d.Migrate(context.Background(), target); err != nil {
			t.Fatalf("migrate to schema %d: %v", target, err)
		}
	}
}
