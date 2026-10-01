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
		"memory exceeded":  {&sgd.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorSMDeliveryFailure), DeliveryFailureCause: &memory}, "sm_delivery_failure", ptr(tgpp.ResultErrorSMDeliveryFailure), ptr(tgpp.VendorID)},
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

func TestAttemptDetails(t *testing.T) {
	protocolError, notSMEquipped, unknownCause := sgd.CauseEquipmentProtocolError, sgd.CauseEquipmentNotSMEquipped, sgd.DeliveryFailureCause(9)
	deliveryFailure := func(cause *sgd.DeliveryFailureCause, diagnostic []byte) error {
		return &sgd.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorSMDeliveryFailure), DeliveryFailureCause: cause, DiagnosticInfo: diagnostic}
	}

	tests := map[string]struct {
		err  error
		want db.DeliveryAttempt
	}{
		"protocol error without report": {deliveryFailure(&protocolError, nil), db.DeliveryAttempt{FailureCause: "equipment_protocol_error"}},
		"protocol error with report": {
			deliveryFailure(&protocolError, []byte{0x00, 0xd0, 0x00}),
			db.DeliveryAttempt{FailureCause: "equipment_protocol_error", TPFailureCause: "usim_sms_storage_full"},
		},
		"application specific report": {
			deliveryFailure(&protocolError, []byte{0x00, 0xe5, 0x00}),
			db.DeliveryAttempt{FailureCause: "equipment_protocol_error", TPFailureCause: "application_specific_229"},
		},
		"reserved report value": {deliveryFailure(&protocolError, []byte{0x00, 0x10, 0x00}), db.DeliveryAttempt{FailureCause: "equipment_protocol_error", TPFailureCause: "unknown_16"}},
		"not a deliver report":  {deliveryFailure(&protocolError, []byte{0x01, 0xd0, 0x00}), db.DeliveryAttempt{FailureCause: "equipment_protocol_error"}},
		"not SM equipped":       {deliveryFailure(&notSMEquipped, nil), db.DeliveryAttempt{FailureCause: "equipment_not_sm_equipped"}},
		"unknown cause":         {deliveryFailure(&unknownCause, nil), db.DeliveryAttempt{FailureCause: "unknown_9"}},
		"missing cause":         {deliveryFailure(nil, nil), db.DeliveryAttempt{}},
		"other vendor": {
			&sgd.ResultError{Result: tgpp.Result{Code: tgpp.ResultErrorSMDeliveryFailure, Experimental: true, VendorID: 42}, DeliveryFailureCause: &protocolError},
			db.DeliveryAttempt{},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := deliveryAttemptOf(kindMME, tc.err)

			if a.FailureCause != tc.want.FailureCause || a.TPFailureCause != tc.want.TPFailureCause ||
				a.AbsentUserDiagnostics != (db.AbsentUserDiagnostics{}) {
				t.Fatalf("attempt = %+v", a)
			}
		})
	}
}

func TestDeliveryAttemptDiagnostics(t *testing.T) {
	detached, noPaging, unknownDiagnostic := tgpp.AbsentUserIMSIDetached, tgpp.AbsentUserNoPagingResponseMSC, tgpp.AbsentUserDiagnostic(99)

	absent := func(diagnostic *tgpp.AbsentUserDiagnostic) error {
		return &sgd.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), AbsentUserDiagnostic: diagnostic}
	}

	tests := map[string]struct {
		kind     nodeKind
		err      error
		nodeType db.NodeType
		want     db.AbsentUserDiagnostics
	}{
		"MME":                     {kindMME, absent(&noPaging), db.NodeTypeMME, db.AbsentUserDiagnostics{MME: "no_paging_response_msc"}},
		"SGSN":                    {kindSGSN, absent(&detached), db.NodeTypeSGSN, db.AbsentUserDiagnostics{SGSN: "imsi_detached"}},
		"SMSF for 3GPP access":    {kindSMSF3GPP, absent(&noPaging), db.NodeTypeSMSF3GPP, db.AbsentUserDiagnostics{SMSF3GPP: "no_paging_response_msc"}},
		"SMSF for non-3GPP":       {kindSMSFNon3GPP, absent(&detached), db.NodeTypeSMSFNon3GPP, db.AbsentUserDiagnostics{SMSFNon3GPP: "imsi_detached"}},
		"unknown reason":          {kindMME, absent(&unknownDiagnostic), db.NodeTypeMME, db.AbsentUserDiagnostics{MME: "unknown_99"}},
		"absent without reason":   {kindSMSF3GPP, absent(nil), db.NodeTypeSMSF3GPP, db.AbsentUserDiagnostics{}},
		"diagnostic on busy user": {kindMME, &sgd.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorUserBusyForMTSMS), AbsentUserDiagnostic: &detached}, db.NodeTypeMME, db.AbsentUserDiagnostics{}},
		"other vendor": {
			kindMME, &sgd.ResultError{Result: tgpp.Result{Code: tgpp.ResultErrorAbsentUser, Experimental: true, VendorID: 42}, AbsentUserDiagnostic: &detached},
			db.NodeTypeMME,
			db.AbsentUserDiagnostics{},
		},
		"success": {kindSMSF3GPP, nil, db.NodeTypeSMSF3GPP, db.AbsentUserDiagnostics{}},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := deliveryAttemptOf(tc.kind, tc.err)

			if a.NodeType != tc.nodeType || a.AbsentUserDiagnostics != tc.want {
				t.Fatalf("attempt = %+v", a)
			}
		})
	}
}

func TestRoutingAttemptDiagnostics(t *testing.T) {
	detached, purged, noPaging, unknownDiagnostic := tgpp.AbsentUserIMSIDetached, tgpp.AbsentUserPurgedNonGPRS, tgpp.AbsentUserNoPagingResponseMSC, tgpp.AbsentUserDiagnostic(99)

	tests := map[string]struct {
		routing s6c.Routing
		err     error
		want    db.AbsentUserDiagnostics
	}{
		"absent at the HSS": {
			err:  &s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), Absent: s6c.AbsentUserDiagnostics{MME: &detached, SMSF3GPP: &purged}},
			want: db.AbsentUserDiagnostics{MME: "imsi_detached", SMSF3GPP: "ms_purged_non_gprs"},
		},
		"all HSS slots": {
			err: &s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser), Absent: s6c.AbsentUserDiagnostics{
				MME: &detached, MSC: &noPaging, SGSN: &purged, SMSF3GPP: &unknownDiagnostic, SMSFNon3GPP: &detached,
			}},
			want: db.AbsentUserDiagnostics{
				MME: "imsi_detached", MSC: "no_paging_response_msc", SGSN: "ms_purged_non_gprs", SMSF3GPP: "unknown_99", SMSFNon3GPP: "imsi_detached",
			},
		},
		"stored diagnostic on success": {
			routing: s6c.Routing{IMSI: "001010000000001", Absent: s6c.AbsentUserDiagnostics{SMSF3GPP: &purged}},
			want:    db.AbsentUserDiagnostics{SMSF3GPP: "ms_purged_non_gprs"},
		},
		"success without diagnostics": {routing: s6c.Routing{IMSI: "001010000000001"}},
		"absent without diagnostics":  {err: &s6c.ResultError{Result: tgpp.Experimental(tgpp.ResultErrorAbsentUser)}},
		"transport error":             {err: diameter.ErrNotConnected},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := routingAttemptOf(tc.routing, tc.err)

			if a.AbsentUserDiagnostics != tc.want || a.NodeType != "" {
				t.Fatalf("attempt = %+v", a)
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
