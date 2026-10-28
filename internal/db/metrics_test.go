package db

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestMetricsCountFailuresOnly(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now()

	if _, err := d.GetSettings(ctx); err != nil {
		t.Fatal(err)
	}

	// A message that is not there and a duplicate are answers, not failures.
	if _, err := d.GetMessage(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMessage of a missing message: %v, want ErrNotFound", err)
	}

	if _, err := d.CreateMessage(ctx, testMessage(1, true, []byte{0x01}, now)); err != nil {
		t.Fatal(err)
	}

	if _, err := d.CreateMessage(ctx, testMessage(1, true, []byte{0x01}, now)); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("CreateMessage of a duplicate: %v, want ErrDuplicate", err)
	}

	if got := testutil.ToFloat64(d.metrics.errors); got != 0 {
		t.Errorf("errors = %v, want 0", got)
	}

	if got := sampleCount(t, d.metrics.duration); got != 4 {
		t.Errorf("duration has %d observations, want 4", got)
	}

	// A call on a closed database fails.
	if err := d.conn.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := d.GetSettings(ctx); err == nil {
		t.Fatal("GetSettings on a closed database succeeded")
	}

	if got := testutil.ToFloat64(d.metrics.errors); got != 1 {
		t.Errorf("errors = %v, want 1", got)
	}
}

func TestMetricsStorage(t *testing.T) {
	d := openTestDB(t)

	problems, err := testutil.CollectAndLint(storageCollector{path: d.path})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range problems {
		t.Errorf("lint: %s: %s", p.Metric, p.Text)
	}

	if n := testutil.CollectAndCount(storageCollector{path: d.path}); n != 2 {
		t.Fatalf("storage has %d series, want 2", n)
	}

	// A file that is not there, such as a write-ahead log that SQLite removed, is empty.
	if n, err := fileSize(filepath.Join(t.TempDir(), "missing")); err != nil || n != 0 {
		t.Fatalf("fileSize of a missing file = %d, %v, want 0, nil", n, err)
	}

	if n, err := fileSize(d.path); err != nil || n == 0 {
		t.Fatalf("fileSize of the database = %d, %v, want more than 0", n, err)
	}
}

func TestMetricsLint(t *testing.T) {
	d := openTestDB(t)

	for _, c := range d.Collectors() {
		problems, err := testutil.CollectAndLint(c)
		if err != nil {
			t.Fatal(err)
		}

		for _, p := range problems {
			t.Errorf("lint: %s: %s", p.Metric, p.Text)
		}
	}
}

// TestEveryCallIsObserved checks that each exported method of DB that calls the database starts with
// defer d.observe(...)(), so that a new one cannot be left out of the metrics.
func TestEveryCallIsObserved(t *testing.T) {
	// CreateMessage calls CreateMessages, which is observed, so that a call is not counted twice. Ping is the
	// readiness probe's, which would fill the query metrics with pings.
	unobserved := map[string]bool{"Close": true, "Collectors": true, "CreateMessage": true, "Ping": true}

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

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || unobserved[fn.Name.Name] || !onDB(fn) {
				continue
			}

			if !startsObserved(fn) {
				t.Errorf("%s: DB.%s does not start with defer d.observe(...)()", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
}

func sampleCount(t *testing.T, h prometheus.Histogram) uint64 {
	t.Helper()

	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatal(err)
	}

	return m.GetHistogram().GetSampleCount()
}

func onDB(fn *ast.FuncDecl) bool {
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}

	id, ok := star.X.(*ast.Ident)

	return ok && id.Name == "DB"
}

func startsObserved(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}

	d, ok := fn.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}

	call, ok := d.Call.Fun.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)

	return ok && sel.Sel.Name == "observe"
}

// messageCounts returns the counts of messages completed, by type and status, that are not zero.
func messageCounts(t *testing.T, d *DB) map[string]float64 {
	t.Helper()

	counts := map[string]float64{}

	for kind, statuses := range finalStatuses {
		for _, status := range statuses {
			if v := testutil.ToFloat64(d.metrics.completed.WithLabelValues(string(kind), string(status))); v != 0 {
				counts[string(kind)+"/"+string(status)] = v
			}
		}
	}

	return counts
}

func TestMetricsCountWhatMessagesBecome(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	delivered, err := d.CreateMessage(ctx, testMessage(1, false, []byte{0x01}, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, delivered, StatusDelivered, now, testReport(now)); err != nil {
		t.Fatal(err)
	}

	// A message retried, then failed.
	retried, err := d.CreateMessage(ctx, testMessage(2, false, []byte{0x02}, now))
	if err != nil {
		t.Fatal(err)
	}

	if err := d.ScheduleRetry(ctx, retried, now, now, false); err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, retried, StatusFailed, now, &NewStatusReport{
		Originator: testServiceCentre, Status: 0x62, TPDU: []byte{0x06}, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// A message replaced, with a report on it.
	replaced, err := d.CreateMessage(ctx, replaceType(testMessage(3, false, []byte{0x03}, now)))
	if err != nil {
		t.Fatal(err)
	}

	replacement := replaceType(testMessage(4, false, []byte{0x04}, now))
	replacement.ReportReplaced = func(Message) (*NewStatusReport, error) {
		r := testReport(now)
		r.Status = 0x02

		return r, nil
	}

	if _, err := d.CreateMessage(ctx, replacement); err != nil {
		t.Fatal(err)
	}

	// A report delivered.
	reports, err := listStatusReports(ctx, d, delivered)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %+v, %v", reports, err)
	}

	if err := d.CompleteMessage(ctx, reports[0].ID, StatusDelivered, now, nil); err != nil {
		t.Fatal(err)
	}

	want := map[string]float64{
		"message/delivered":       1,
		"message/failed":          1,
		"message/replaced":        1,
		"status_report/delivered": 1,
	}

	if got := messageCounts(t, d); !reflect.DeepEqual(got, want) {
		t.Fatalf("counts = %v, want %v", got, want)
	}

	// Neither a message no longer pending, nor a transaction rolled back, counts.
	if err := d.CompleteMessage(ctx, replaced, StatusDelivered, now, nil); !errors.Is(err, ErrNotPending) {
		t.Fatalf("err = %v, want ErrNotPending", err)
	}

	fromAPI := testMessage(0, false, []byte{0x05}, now)
	fromAPI.Origin = OriginAPI

	id, err := d.CreateMessage(ctx, fromAPI)
	if err != nil {
		t.Fatal(err)
	}

	if err := d.CompleteMessage(ctx, id, StatusDelivered, now, testReport(now)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	if got := messageCounts(t, d); !reflect.DeepEqual(got, want) {
		t.Fatalf("counts after failed calls = %v, want %v", got, want)
	}
}
