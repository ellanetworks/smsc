package db

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/numbering"
	"github.com/ellanetworks/smsc/internal/settings"
)

func TestDefaultSettings(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetSettings(context.Background())
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}

	want := settings.Settings{
		Operator: settings.Operator{
			MCC:                  "001",
			MNC:                  "01",
			ServiceCentreAddress: "15550000000",
			Numbering:            numbering.Plan{CountryCode: "1", NationalPrefix: "1", InternationalPrefix: "011"},
		},
		Delivery: settings.Delivery{
			DefaultValidity: 7 * 24 * time.Hour,
			RetryIntervals:  []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSettings = %+v, want %+v", got, want)
	}

	if err := got.Validate(); err != nil {
		t.Fatalf("default settings are invalid: %v", err)
	}
}

func TestUpdateSettingsPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "smsc.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	want := settings.Settings{
		Operator: settings.Operator{
			MCC:                  "208",
			MNC:                  "010",
			ServiceCentreAddress: "33600000000",
			Numbering:            numbering.Plan{CountryCode: "33", NationalPrefix: "0", InternationalPrefix: "00"},
		},
		Delivery: settings.Delivery{
			DefaultValidity: 48 * time.Hour,
			RetryIntervals:  []time.Duration{500 * time.Millisecond, 10 * time.Minute},
		},
	}

	if err := d.UpdateOperator(ctx, want.Operator); err != nil {
		t.Fatalf("UpdateOperator: %v", err)
	}

	if err := d.UpdateDelivery(ctx, want.Delivery); err != nil {
		t.Fatalf("UpdateDelivery: %v", err)
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = d.Close() })

	got, err := d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSettings after reopening = %+v, want %+v", got, want)
	}

	if err := d.UpdateDelivery(ctx, settings.Delivery{DefaultValidity: time.Hour}); err != nil {
		t.Fatalf("UpdateDelivery: %v", err)
	}

	got, err = d.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Delivery.RetryIntervals) != 0 || got.Operator != want.Operator {
		t.Fatalf("after clearing the retry intervals = %+v", got)
	}
}
