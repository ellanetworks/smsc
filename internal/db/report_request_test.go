package db

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/tpdu"
)

func submitTo(t *testing.T, reference uint8, destination tpdu.Address, srr bool) []byte {
	t.Helper()

	b, err := tpdu.Submit{
		StatusReportRequest: srr,
		MessageReference:    reference,
		ProtocolIdentifier:  0x41,
		Destination:         destination,
		UserData:            []byte("hello"),
		UserDataLength:      5,
	}.Encode()
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func fromMS(t *testing.T, reference uint8, destination tpdu.Address, srr bool, at time.Time) NewMessage {
	t.Helper()

	m := testMessage(reference, false, submitTo(t, reference, destination, srr), at)
	m.Recipient = Address{
		Digits: destination.Digits, TypeOfNumber: destination.TypeOfNumber, NumberingPlan: destination.NumberingPlan,
	}

	return m
}

func TestStatusReportFromStoredFields(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	discharge := at.Add(90 * time.Second)

	for _, destination := range []tpdu.Address{
		{TypeOfNumber: tonInternational, NumberingPlan: 1, Digits: "15551230002"},
		{TypeOfNumber: tonNational, NumberingPlan: 1, Digits: "5551230002"},
		{TypeOfNumber: tonUnknown, NumberingPlan: 1, Digits: "1234"},
		{TypeOfNumber: tonUnknown, NumberingPlan: 0, Digits: "123"},
		{TypeOfNumber: tonUnknown, NumberingPlan: 1, Digits: "*#12"},
	} {
		id, err := d.CreateMessage(ctx, fromMS(t, 9, destination, true, at))
		if err != nil {
			t.Fatalf("%+v: %v", destination, err)
		}

		m, err := d.GetMessage(ctx, id)
		if err != nil || !m.StatusReportRequest {
			t.Fatalf("%+v: message = %+v, %v", destination, m, err)
		}

		report, err := StatusReportFor(m, tpdu.StatusReceived, discharge, "15550000000", time.Hour)
		if err != nil || report == nil {
			t.Fatalf("%+v: report = %v, %v", destination, report, err)
		}

		submit, err := tpdu.DecodeSubmit(m.TPDU)
		if err != nil {
			t.Fatal(err)
		}

		want, err := tpdu.StatusReport{
			MessageReference:       submit.MessageReference,
			Recipient:              submit.Destination,
			ServiceCentreTimeStamp: m.SubmittedAt,
			DischargeTime:          discharge,
			Status:                 tpdu.StatusReceived,
		}.Encode()
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(report.TPDU, want) {
			t.Fatalf("%+v: report %x, want %x", destination, report.TPDU, want)
		}
	}
}

func TestStatusReportOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	destination := tpdu.Address{TypeOfNumber: tonInternational, NumberingPlan: 1, Digits: testMSISDN}

	plain, err := d.CreateMessage(ctx, fromMS(t, 1, destination, false, at))
	if err != nil {
		t.Fatal(err)
	}

	api := fromMS(t, 2, destination, true, at)
	api.Origin = OriginAPI

	fromAPI, err := d.CreateMessage(ctx, api)
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{plain, fromAPI} {
		m, err := d.GetMessage(ctx, id)
		if err != nil || m.StatusReportRequest {
			t.Fatalf("message %d = %+v, %v; want no report requested", id, m, err)
		}

		if report, err := StatusReportFor(m, tpdu.StatusReceived, at, "15550000000", time.Hour); err != nil || report != nil {
			t.Fatalf("message %d: report = %+v, %v; want none", id, report, err)
		}
	}

	if _, err := d.writer.ExecContext(ctx, `UPDATE messages SET status_report_request = 1 WHERE id = ?`, fromAPI); err == nil {
		t.Fatal("a report request on a message from the API")
	}
}
