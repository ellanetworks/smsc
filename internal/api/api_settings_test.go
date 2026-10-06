package api_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/api"
)

const (
	validOperator = `{"mcc": "208", "mnc": "10", "service_centre_address": "+33600000000", "numbering": {"country_code": "33", "national_prefix": "0", "international_prefix": "00"}}`
	validDelivery = `{"default_validity_seconds": 172800, "retry_intervals_seconds": [30, 600]}`
)

func TestGetSettings(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	_, result, _ := a.do(t, http.MethodGet, "/api/v1/operator", "")
	if got, want := decode[api.OperatorSettings](t, result), (api.OperatorSettings{
		MCC:                  "001",
		MNC:                  "01",
		ServiceCentreAddress: "+15550000000",
		Numbering:            api.NumberingSettings{CountryCode: "1"},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("operator = %+v, want %+v", got, want)
	}

	_, result, _ = a.do(t, http.MethodGet, "/api/v1/delivery", "")
	if got, want := decode[api.DeliverySettings](t, result), (api.DeliverySettings{
		DefaultValiditySeconds: 86400,
		RetryIntervalsSeconds:  []int64{60, 300, 900, 3600},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery = %+v, want %+v", got, want)
	}
}

func TestUpdateSettings(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	for path, body := range map[string]string{
		"/api/v1/operator": validOperator,
		"/api/v1/delivery": validDelivery,
	} {
		if code, _, errMsg := a.do(t, http.MethodPut, path, body); code != http.StatusOK {
			t.Fatalf("PUT %s = %d %q", path, code, errMsg)
		}
	}

	stored, err := a.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if stored.Operator.ServiceCentreAddress != "33600000000" || stored.Operator.Realm() != "epc.mnc010.mcc208.3gppnetwork.org" ||
		stored.Delivery.DefaultValidity != 48*time.Hour || len(stored.Delivery.RetryIntervals) != 2 {
		t.Fatalf("stored = %+v", stored)
	}

	a.create(t, "+15550001111", "+15551230002", "hello")

	m, err := a.store.GetMessage(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}

	if got := m.ExpiresAt.Sub(m.SubmittedAt); got != 48*time.Hour {
		t.Fatalf("validity of a new message = %s, want the updated 48h", got)
	}
}

func TestUpdateSettingsKeepsOtherAreas(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	if code, _, errMsg := a.do(t, http.MethodPut, "/api/v1/operator", validOperator); code != http.StatusOK {
		t.Fatalf("PUT operator = %d %q", code, errMsg)
	}

	_, result, _ := a.do(t, http.MethodGet, "/api/v1/delivery", "")
	if got := decode[api.DeliverySettings](t, result); got.DefaultValiditySeconds != 86400 {
		t.Fatalf("delivery changed by an operator update: %+v", got)
	}
}

func TestUpdateSettingsRejectsInvalidInput(t *testing.T) {
	tests := map[string]struct{ path, body, from, to, want string }{
		"not JSON":                {"/api/v1/operator", validOperator, `{`, `[`, "Invalid request data"},
		"unknown field":           {"/api/v1/operator", validOperator, `"numbering"`, `"unknown": 1, "numbering"`, "Invalid request data"},
		"short mcc":               {"/api/v1/operator", validOperator, `"mcc": "208"`, `"mcc": "20"`, "mcc must be 3 digits"},
		"long mnc":                {"/api/v1/operator", validOperator, `"mnc": "10"`, `"mnc": "0010"`, "mnc must be 2 or 3 digits"},
		"missing sc address":      {"/api/v1/operator", validOperator, `"+33600000000"`, `""`, "service_centre_address must be an E.164 number such as +15550000000"},
		"sc address without +":    {"/api/v1/operator", validOperator, `"+33600000000"`, `"33600000000"`, "service_centre_address must be an E.164 number such as +15550000000"},
		"long sc address":         {"/api/v1/operator", validOperator, `"+33600000000"`, `"+3360000000000000"`, "service_centre_address must be an E.164 number such as +15550000000"},
		"long country code":       {"/api/v1/operator", validOperator, `"country_code": "33"`, `"country_code": "3333"`, "numbering.country_code must be 1 to 3 digits"},
		"bad national prefix":     {"/api/v1/operator", validOperator, `"national_prefix": "0"`, `"national_prefix": "+"`, "numbering.national_prefix must be digits"},
		"bad intl prefix":         {"/api/v1/operator", validOperator, `"international_prefix": "00"`, `"international_prefix": "+"`, "numbering.international_prefix must be digits"},
		"zero validity":           {"/api/v1/delivery", validDelivery, `172800`, `0`, "default_validity_seconds must be between 1 and 9223372036"},
		"overflowing validity":    {"/api/v1/delivery", validDelivery, `172800`, `9223372037`, "default_validity_seconds must be between 1 and 9223372036"},
		"zero retry interval":     {"/api/v1/delivery", validDelivery, `[30, 600]`, `[30, 0]`, "retry_intervals_seconds must be between 1 and 9223372036"},
		"negative retry interval": {"/api/v1/delivery", validDelivery, `[30, 600]`, `[-30]`, "retry_intervals_seconds must be between 1 and 9223372036"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := newTestAPI(t, fakeDiameter{})

			body := strings.Replace(tc.body, tc.from, tc.to, 1)
			if body == tc.body {
				t.Fatalf("test edit %q did not apply", tc.from)
			}

			_, before, _ := a.do(t, http.MethodGet, tc.path, "")

			code, _, errMsg := a.do(t, http.MethodPut, tc.path, body)
			if code != http.StatusBadRequest || errMsg != tc.want {
				t.Fatalf("put = %d %q, want 400 %q", code, errMsg, tc.want)
			}

			if _, after, _ := a.do(t, http.MethodGet, tc.path, ""); string(after) != string(before) {
				t.Fatalf("settings changed after a rejected update: %s -> %s", before, after)
			}
		})
	}
}

func TestUpdateDeliveryAcceptsNoRetries(t *testing.T) {
	a := newTestAPI(t, fakeDiameter{})

	body := strings.Replace(validDelivery, `[30, 600]`, `[]`, 1)

	code, result, errMsg := a.do(t, http.MethodPut, "/api/v1/delivery", body)
	if code != http.StatusOK {
		t.Fatalf("put = %d %q", code, errMsg)
	}

	if got := decode[api.DeliverySettings](t, result); len(got.RetryIntervalsSeconds) != 0 {
		t.Fatalf("delivery = %+v", got)
	}
}
