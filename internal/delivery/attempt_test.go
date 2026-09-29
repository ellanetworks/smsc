package delivery

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ellanetworks/core/diameter"
	"github.com/ellanetworks/core/diameter/s6c"
	"github.com/ellanetworks/core/diameter/sgd"
	"github.com/ellanetworks/core/diameter/tgpp"
	"github.com/ellanetworks/smsc/internal/db"
	smscs6c "github.com/ellanetworks/smsc/internal/s6c"
)

func TestAttemptOutcomes(t *testing.T) {
	memory := sgd.CauseMemoryCapacityExceeded

	tests := map[string]struct {
		err        error
		outcome    string
		resultCode *uint32
		vendorID   *uint32
	}{
		"success":          {nil, "success", ptr(diameter.ResultSuccess), nil},
		"absent user":      {&s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser)}, "absent_user", ptr(tgpp.ResultErrorAbsentUser), ptr(tgpp.VendorID)},
		"user unknown":     {&s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorUserUnknown)}, "user_unknown", ptr(tgpp.ResultErrorUserUnknown), ptr(tgpp.VendorID)},
		"base 5001":        {&s6c.ResultError{Result: tgpp.Result{Code: 5001}}, "diameter_error", ptr(uint32(5001)), nil},
		"too busy":         {&sgd.ResultError{Result: tgpp.Result{Code: diameter.ResultTooBusy}}, "diameter_error", ptr(diameter.ResultTooBusy), nil},
		"other vendor":     {&s6c.ResultError{Result: tgpp.Result{Code: 5550, Experimental: true, VendorID: 42}}, "diameter_error", ptr(uint32(5550)), ptr(uint32(42))},
		"memory exceeded":  {&sgd.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorSMDeliveryFailure), DeliveryFailureCause: &memory}, "memory_capacity_exceeded", ptr(tgpp.ResultErrorSMDeliveryFailure), ptr(tgpp.VendorID)},
		"delivery failure": {&sgd.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorSMDeliveryFailure)}, "sm_delivery_failure", ptr(tgpp.ResultErrorSMDeliveryFailure), ptr(tgpp.VendorID)},
		"no HSS":           {fmt.Errorf("s6c: %w: %w", smscs6c.ErrNoHSS, context.DeadlineExceeded), "no_hss", nil, nil},
		"not connected":    {fmt.Errorf("%w: %w", diameter.ErrNotConnected, context.DeadlineExceeded), "unreachable", nil, nil},
		"unknown peer":     {diameter.ErrUnknownPeer, "unreachable", nil, nil},
		"timeout":          {context.DeadlineExceeded, "timeout", nil, nil},
		"malformed":        {errors.New("malformed answer"), "error", nil, nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := attemptOf(tc.err)

			if a.Outcome != tc.outcome || !equalPtr(a.ResultCode, tc.resultCode) || !equalPtr(a.VendorID, tc.vendorID) {
				t.Fatalf("attempt = %s %v %v", a.Outcome, a.ResultCode, a.VendorID)
			}
		})
	}
}

func equalPtr(a, b *uint32) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func TestRoutingFailureIsRecorded(t *testing.T) {
	store := newFakeStore(pendingMessage(t))
	router := &fakeRouter{err: &s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser)}}

	process(t, newDeliverer(store, router, &fakeSender{}))

	got := store.recorded("")
	if len(got) != 1 || got[0] != (attempt{db.StepRouting, "", "absent_user"}) {
		t.Fatalf("attempts = %+v", got)
	}
}
