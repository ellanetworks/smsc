package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/netip"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ellanetworks/smsc/internal/settings"
)

type recorder struct {
	d       *DB
	entries [][]byte
	last    int64
}

func (r *recorder) Propose(_ context.Context, cmds []Command) ([]Result, error) {
	r.last++

	entry, err := EncodeBatch(Batch{LeaderTime: r.last, Commands: cmds})
	if err != nil {
		return nil, err
	}

	results, err := r.d.Apply(r.d.AppliedIndex()+1, 1, entry)
	if err == nil {
		r.entries = append(r.entries, entry)
	}

	return results, err
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}

	return v
}

func runWorkload(t *testing.T, d *DB) {
	t.Helper()

	ctx := context.Background()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	must(d.Secrets(ctx))

	admin := must(d.CreateUser(ctx, NewUser{Email: "admin@example.com", Role: RoleAdmin, PasswordHash: "h"}, 10))
	ota := must(d.CreateUser(ctx, NewUser{Email: "ota@example.com", Role: RoleSubmitter, PasswordHash: "h"}, 10))

	if _, err := d.CreateUser(ctx, NewUser{Email: "admin@example.com", Role: RoleAdmin}, 10); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate user = %v", err)
	}

	must(d.CreateSession(ctx, NewSession{
		UserID: admin, TokenHash: []byte("token"), Method: SessionPassword, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}, 5))

	token := must(d.CreateAPIToken(ctx, NewAPIToken{
		TokenID: "t1", Name: "ci", TokenHash: []byte("hash"), UserID: admin, CreatedAt: now,
	}))

	for _, err := range []error{
		d.TouchAPIToken(ctx, token, now.Add(time.Minute)),
		d.UpdateUserRole(ctx, ota, RoleReadOnly, true),
		d.UpdateUserPassword(ctx, ota, "h2", nil),
		d.DeleteSessionByTokenHash(ctx, []byte("token")),
		d.UpdateDelivery(ctx, settings.Delivery{DefaultValidity: time.Hour, RetryIntervals: []time.Duration{time.Minute}}),
		d.UpdateRetention(ctx, settings.Retention{Messages: 48 * time.Hour}),
		d.UseOIDCState(ctx, bytes.Repeat([]byte{1}, 32), now.Add(time.Minute), now),
		d.UpdateOIDC(ctx, OIDC{Scopes: []string{"openid"}, SessionLifetime: time.Hour}),
		d.DeleteExpiredSessions(ctx, now),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	peer := testPeer()
	peer.ID = must(d.CreatePeer(ctx, peer))
	peer.Port = 3869

	if err := d.UpdatePeer(ctx, peer); err != nil {
		t.Fatal(err)
	}

	esme := must(d.CreateESME(ctx, ESME{
		SystemID: "ota", UserID: ota, PasswordHash: "x", CreatedAt: now,
		AllowedNetworks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
	}))

	first := must(d.CreateMessage(ctx, testMessage(1, false, reportedSubmit(t, 1), now)))

	if _, err := d.CreateMessage(ctx, testMessage(1, true, reportedSubmit(t, 1), now)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate message = %v", err)
	}

	replacement := replaceType(testMessage(2, false, reportedSubmit(t, 2), now.Add(time.Second)))
	replacement.ReportReplaced = true

	must(d.CreateMessage(ctx, replaceType(testMessage(3, false, reportedSubmit(t, 3), now))))
	must(d.CreateMessage(ctx, replacement))

	smpp := testMessage(0, false, []byte{0x00}, now)
	smpp.Origin, smpp.SubmittedBy = OriginSMPP, ota
	smpp.SMPP = &NewSMPPMessage{ESMEID: esme, Receipt: ReceiptFinal}
	smppID := must(d.CreateMessage(ctx, smpp))

	must(d.CreateDeliveryAttempt(ctx, DeliveryAttempt{
		MessageID: first, StartedAt: now, CompletedAt: now, Step: StepDelivery, Node: "mme", Outcome: "absent",
	}))

	must(d.AlertRecipient(ctx, testMSISDN, now))

	for _, err := range []error{
		d.SetAlertMSISDN(ctx, testMSISDN, "15559990000", now),
		d.ScheduleRetry(ctx, first, now.Add(time.Minute), now, true, time.Time{}, Fence{}),
		d.CompleteMessage(ctx, first, StatusDelivered, now.Add(2*time.Minute), testReport(now), 0, Fence{}),
		d.CompleteMessage(ctx, smppID, StatusFailed, now.Add(2*time.Minute), nil, 0x45, Fence{}),
		d.UpdateESME(ctx, ESME{ID: esme}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := d.CompleteMessage(ctx, first, StatusDelivered, now, nil, 0, Fence{}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second completion = %v", err)
	}

	must(d.ExpireNotifications(ctx, now.Add(365*24*time.Hour)))
	must(d.PurgeMessages(ctx, now.Add(365*24*time.Hour), 1000))

	if err := d.DeletePeer(ctx, peer.ID); err != nil {
		t.Fatal(err)
	}

	if err := d.DeleteUser(ctx, ota); err != nil {
		t.Fatal(err)
	}
}

func dump(t *testing.T, d *DB) string {
	t.Helper()

	ctx := context.Background()

	rows, err := d.reader().QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}

	var tables []string

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}

		tables = append(tables, name)
	}

	_ = rows.Close()

	var b strings.Builder

	for _, table := range tables {
		rows, err := d.reader().QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}

		cols, _ := rows.Columns()

		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))

			for i := range values {
				ptrs[i] = &values[i]
			}

			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}

			fmt.Fprintf(&b, "%s %v\n", table, values)
		}

		_ = rows.Close()
	}

	return b.String()
}

func TestApplyIsDeterministic(t *testing.T) {
	leader := openTestDB(t)
	r := &recorder{d: leader}
	leader.SetProposer(r)

	runWorkload(t, leader)

	replica := openTestDB(t)

	for i, entry := range r.entries {
		if _, err := replica.Apply(uint64(i+1), 1, entry); err != nil {
			t.Fatalf("entry %d: %v", i+1, err)
		}
	}

	if leaderIndex, replicaIndex := leader.AppliedIndex(), replica.AppliedIndex(); leaderIndex != uint64(len(r.entries)) ||
		replicaIndex != leaderIndex {
		t.Fatalf("leader at %d, replica at %d", leaderIndex, replicaIndex)
	}

	if a, b := dump(t, leader), dump(t, replica); a != b {
		t.Fatalf("replica differs from the leader:\nleader:\n%s\nreplica:\n%s", a, b)
	}
}

func TestGroupedEntriesMatchSingleEntries(t *testing.T) {
	leader := openTestDB(t)
	r := &recorder{d: leader}
	leader.SetProposer(r)

	runWorkload(t, leader)

	replica := openTestDB(t)

	for i := 0; i < len(r.entries); i += 3 {
		var group []Entry

		for j := i; j < min(i+3, len(r.entries)); j++ {
			group = append(group, Entry{Index: uint64(j + 1), Term: 1, Data: r.entries[j]})
		}

		if _, err := replica.ApplyEntries(group); err != nil {
			t.Fatalf("entries from %d: %v", i+1, err)
		}
	}

	if leaderIndex, replicaIndex := leader.AppliedIndex(), replica.AppliedIndex(); replicaIndex != leaderIndex {
		t.Fatalf("leader at %d, replica at %d", leaderIndex, replicaIndex)
	}

	if a, b := dump(t, leader), dump(t, replica); a != b {
		t.Fatalf("replica differs from the leader:\nleader:\n%s\nreplica:\n%s", a, b)
	}
}

func TestExpiredRequestIDIsAppliedAgainInAnyGrouping(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	id := RequestID{7}

	cmd := store(t, id, testMessage(1, false, []byte{0}, now))
	cmd.Retryable = true

	var entries [][]byte

	for _, at := range []time.Time{now, now.Add(appliedRequestsWindow + time.Second)} {
		entry, err := EncodeBatch(Batch{LeaderTime: at.UnixNano(), Commands: []Command{cmd}})
		if err != nil {
			t.Fatal(err)
		}

		entries = append(entries, entry)
	}

	single, grouped := openTestDB(t), openTestDB(t)

	for i, e := range entries {
		if _, err := single.Apply(uint64(i+1), 1, e); err != nil {
			t.Fatal(err)
		}
	}

	results, err := grouped.ApplyEntries([]Entry{{Index: 1, Term: 1, Data: entries[0]}, {Index: 2, Term: 1, Data: entries[1]}})
	if err != nil {
		t.Fatal(err)
	}

	if results[1][0].Replayed {
		t.Fatal("an expired request ID was replayed")
	}

	if a, b := dump(t, single), dump(t, grouped); a != b {
		t.Fatalf("grouping changed the state:\nsingle:\n%s\ngrouped:\n%s", a, b)
	}
}

func store(t *testing.T, id RequestID, messages ...NewMessage) Command {
	t.Helper()

	p, err := json.Marshal(storeMessages{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}

	return Command{Type: cmdStoreMessages, Version: 1, RequestID: id, Payload: p}
}

func applyBatch(t *testing.T, d *DB, cmds ...Command) []Result {
	t.Helper()

	entry, err := EncodeBatch(Batch{LeaderTime: 1, Commands: cmds})
	if err != nil {
		t.Fatal(err)
	}

	results, err := d.Apply(d.AppliedIndex()+1, 1, entry)
	if err != nil {
		t.Fatal(err)
	}

	return results
}

func TestFailedCommandLeavesNoTrace(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	bad := testMessage(2, false, []byte{0x02}, now)
	bad.Origin = "nowhere"

	results := applyBatch(t, d,
		store(t, RequestID{1}, testMessage(1, false, []byte{0x01}, now)),
		store(t, RequestID{2}, bad),
		store(t, RequestID{3}, testMessage(3, false, []byte{0x03}, now)),
	)

	if results[0].Err() != nil || !errors.Is(results[1].Err(), ErrConstraint) || results[2].Err() != nil {
		t.Fatalf("results = %+v", results)
	}

	messages, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10)
	if err != nil || total != 2 {
		t.Fatalf("messages = %+v, %d, %v; want the two valid ones", messages, total, err)
	}

	if got := messages[0].TimeStamp.Sub(messages[1].TimeStamp); got != time.Second {
		t.Fatalf("time stamps %v apart, want 1s: the failed command's stamp leaked", got)
	}
}

func TestRequestIDReturnsTheFirstResult(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	retried := WithRequestID(ctx, RequestIDOf([]byte("mme.example"), []byte("session;1")))

	first, err := d.CreateMessage(retried, testMessage(1, true, []byte{0x01}, now))
	if err != nil {
		t.Fatal(err)
	}

	again, err := d.CreateMessage(retried, testMessage(1, true, []byte{0x01}, now))
	if err != nil || again != first {
		t.Fatalf("retry = %d, %v; want %d", again, err, first)
	}

	if _, err := d.CreateMessage(ctx, testMessage(1, true, []byte{0x01}, now)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("new request with the same TP-MR = %v, want a duplicate", err)
	}

	if _, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10); err != nil || total != 1 {
		t.Fatalf("%d messages, %v; want one", total, err)
	}
}

func TestRequestIDKeepsAnErrorResult(t *testing.T) {
	ctx := WithRequestID(context.Background(), RequestID{9})
	d := openTestDB(t)

	if err := d.CompleteMessage(ctx, 42, StatusDelivered, time.Now(), nil, 0, Fence{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}

	if err := d.CompleteMessage(ctx, 42, StatusDelivered, time.Now(), nil, 0, Fence{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retried err = %v", err)
	}
}

func TestApplySkipsAppliedEntries(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	entry, err := EncodeBatch(Batch{LeaderTime: 1, Commands: []Command{
		store(t, RequestID{}, testMessage(1, false, []byte{0x01}, now)),
	}})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := d.Apply(1, 1, entry); err != nil {
			t.Fatal(err)
		}
	}

	if _, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10); err != nil || total != 1 || d.AppliedIndex() != 1 {
		t.Fatalf("%d messages at index %d, %v; want one at 1", total, d.AppliedIndex(), err)
	}
}

func TestUnknownCommandIsFatal(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	entry, err := EncodeBatch(Batch{LeaderTime: 1, Commands: []Command{
		store(t, RequestID{}, testMessage(1, false, []byte{0x01}, now)),
		{Type: 9999, Version: 1, Payload: json.RawMessage(`{}`)},
	}})
	if err != nil {
		t.Fatal(err)
	}

	var fatal *FatalError
	if _, err := d.Apply(1, 1, entry); !errors.As(err, &fatal) || fatal.Index != 1 {
		t.Fatalf("err = %v, want a fatal error at index 1", err)
	}

	if _, total, err := d.ListMessages(ctx, MessageFilter{}, 1, 10); err != nil || total != 0 || d.AppliedIndex() != 0 {
		t.Fatalf("%d messages at index %d, %v; want nothing applied", total, d.AppliedIndex(), err)
	}
}

func TestEventsFollowTheCommit(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

	var got []Events

	d.OnEvents(func(ev Events) { got = append(got, ev) })

	must(d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, now)))

	if _, err := d.CreateMessage(ctx, testMessage(1, true, []byte{0x01}, now)); !errors.Is(err, ErrDuplicate) {
		t.Fatal(err)
	}

	must(d.AlertRecipient(ctx, testMSISDN, now))

	if err := d.UpdateRetention(ctx, settings.Retention{Messages: 48 * time.Hour}); err != nil {
		t.Fatal(err)
	}

	if len(got) != 3 || !got[0].Messages || len(got[1].Alerted) != 1 || got[1].Alerted[0] != testMSISDN || !got[2].Settings {
		t.Fatalf("events = %+v; want stored, alerted and settings, and nothing for the duplicate", got)
	}
}

func TestStateSurvivesReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "smsc.db")

	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	must(d.Secrets(ctx))

	index := d.AppliedIndex()

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	d, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = d.Close() }()

	if again := d.AppliedIndex(); index != 1 || again != index {
		t.Fatalf("index %d before, %d after", index, again)
	}
}

var (
	limitClause = regexp.MustCompile(`(?is)ORDER BY\s+([^;]*?)\s+LIMIT\b`)
	uniqueLast  = regexp.MustCompile(`(?i)^(\w+\.)?id(\s+(ASC|DESC))?$`)
)

func TestBoundedQueriesHaveATotalOrder(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}

			s, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.Contains(strings.ToUpper(s), "LIMIT") {
				return true
			}

			matches := limitClause.FindAllStringSubmatch(s, -1)
			if len(matches) != strings.Count(strings.ToUpper(s), "LIMIT") {
				t.Errorf("%s: a LIMIT without an ORDER BY", fset.Position(lit.Pos()))
				return true
			}

			for _, m := range matches {
				terms := strings.Split(m[1], ",")
				if last := strings.TrimSpace(terms[len(terms)-1]); !uniqueLast.MatchString(last) {
					t.Errorf("%s: ORDER BY %s does not end with a unique key", fset.Position(lit.Pos()), m[1])
				}
			}

			return true
		})
	}
}
