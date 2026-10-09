package db

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
	// CreateMessage calls CreateMessages, which is observed, so that a call is not counted twice.
	unobserved := map[string]bool{"Close": true, "Collectors": true, "CreateMessage": true}

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
