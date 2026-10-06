package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/ellanetworks/smsc/internal/numbering"
	"github.com/ellanetworks/smsc/internal/settings"
)

const maxSeconds = math.MaxInt64 / int64(time.Second)

type OperatorSettings struct {
	MCC                  string            `json:"mcc"`
	MNC                  string            `json:"mnc"`
	ServiceCentreAddress string            `json:"service_centre_address"`
	Numbering            NumberingSettings `json:"numbering"`
}

type NumberingSettings struct {
	CountryCode         string `json:"country_code"`
	NationalPrefix      string `json:"national_prefix"`
	InternationalPrefix string `json:"international_prefix"`
}

type DeliverySettings struct {
	DefaultValiditySeconds int64   `json:"default_validity_seconds"`
	RetryIntervalsSeconds  []int64 `json:"retry_intervals_seconds"`
}

func GetOperator(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, operatorResponse(cfg.Settings.Get().Operator), http.StatusOK, cfg.Logger)
	})
}

func UpdateOperator(cfg Config) http.Handler {
	return updateHandler(cfg, "operator", func(ctx context.Context, p OperatorSettings) error {
		address, ok := e164Digits(p.ServiceCentreAddress)
		if !ok {
			return invalid(errors.New("service_centre_address must be an E.164 number such as +15550000000"))
		}

		o := settings.Operator{
			MCC:                  p.MCC,
			MNC:                  p.MNC,
			ServiceCentreAddress: address,
			Numbering: numbering.Plan{
				CountryCode:         p.Numbering.CountryCode,
				NationalPrefix:      p.Numbering.NationalPrefix,
				InternationalPrefix: p.Numbering.InternationalPrefix,
			},
		}

		if err := o.Validate(); err != nil {
			return invalid(err)
		}

		return cfg.Settings.UpdateOperator(ctx, o)
	}, func(s settings.Settings) any { return operatorResponse(s.Operator) })
}

func GetDelivery(cfg Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeResponse(w, deliveryResponse(cfg.Settings.Get().Delivery), http.StatusOK, cfg.Logger)
	})
}

func UpdateDelivery(cfg Config) http.Handler {
	return updateHandler(cfg, "delivery", func(ctx context.Context, p DeliverySettings) error {
		if !validSeconds(p.DefaultValiditySeconds) {
			return invalid(fmt.Errorf("default_validity_seconds must be between 1 and %d", maxSeconds))
		}

		d := settings.Delivery{
			DefaultValidity: time.Duration(p.DefaultValiditySeconds) * time.Second,
			RetryIntervals:  []time.Duration{},
		}

		for _, seconds := range p.RetryIntervalsSeconds {
			if !validSeconds(seconds) {
				return invalid(fmt.Errorf("retry_intervals_seconds must be between 1 and %d", maxSeconds))
			}

			d.RetryIntervals = append(d.RetryIntervals, time.Duration(seconds)*time.Second)
		}

		return cfg.Settings.UpdateDelivery(ctx, d)
	}, func(s settings.Settings) any { return deliveryResponse(s.Delivery) })
}

type invalidError struct{ error }

func invalid(err error) error { return invalidError{err} }

func updateHandler[P any](cfg Config, subject string, apply func(context.Context, P) error,
	respond func(settings.Settings) any,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var params P

		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()

		if err := dec.Decode(&params); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid request data", err, cfg.Logger)
			return
		}

		if err := apply(r.Context(), params); err != nil {
			if bad := (invalidError{}); errors.As(err, &bad) {
				writeError(w, http.StatusBadRequest, bad.Error(), nil, cfg.Logger)
				return
			}

			writeError(w, http.StatusInternalServerError, "Failed to update "+subject, err, cfg.Logger)

			return
		}

		writeResponse(w, respond(cfg.Settings.Get()), http.StatusOK, cfg.Logger)
	})
}

func validSeconds(seconds int64) bool {
	return seconds >= 1 && seconds <= maxSeconds
}

func operatorResponse(o settings.Operator) OperatorSettings {
	return OperatorSettings{
		MCC:                  o.MCC,
		MNC:                  o.MNC,
		ServiceCentreAddress: "+" + o.ServiceCentreAddress,
		Numbering: NumberingSettings{
			CountryCode:         o.Numbering.CountryCode,
			NationalPrefix:      o.Numbering.NationalPrefix,
			InternationalPrefix: o.Numbering.InternationalPrefix,
		},
	}
}

func deliveryResponse(d settings.Delivery) DeliverySettings {
	resp := DeliverySettings{
		DefaultValiditySeconds: int64(d.DefaultValidity / time.Second),
		RetryIntervalsSeconds:  []int64{},
	}

	for _, interval := range d.RetryIntervals {
		resp.RetryIntervalsSeconds = append(resp.RetryIntervalsSeconds, int64(interval/time.Second))
	}

	return resp
}
