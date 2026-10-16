package kube

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestTableToFrameWideColumns(t *testing.T) {
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME"}, {Name: "STATUS"}, {Name: "NODE", Priority: 1},
		},
		Rows: []metav1.TableRow{{Cells: []interface{}{"p", "Running", "n1"}}},
	}
	if f := tableToFrame(tbl, false, false); !equalStr(f.Headers, []string{"NAME", "STATUS"}) || !equalStr(f.Rows[0], []string{"p", "Running"}) {
		t.Errorf("non-wide frame leaked wide column: %v %v", f.Headers, f.Rows)
	}
	if f := tableToFrame(tbl, false, true); !equalStr(f.Headers, []string{"NAME", "STATUS", "NODE"}) || len(f.Rows[0]) != 3 {
		t.Errorf("wide frame missing column: %v %v", f.Headers, f.Rows)
	}
}

// A render slower than the debounce window must never overlap with another
// render; extra kicks coalesce into a single follow-up run.
func TestRefresherSerializesAndCoalesces(t *testing.T) {
	var running, maxRunning, runs atomic.Int32
	started := make(chan struct{}, 64)
	kick, stop := startRefresher(context.Background(), 5*time.Millisecond, func(context.Context) {
		n := running.Add(1)
		for {
			m := maxRunning.Load()
			if n <= m || maxRunning.CompareAndSwap(m, n) {
				break
			}
		}
		runs.Add(1)
		started <- struct{}{}
		time.Sleep(60 * time.Millisecond) // slower than the debounce
		running.Add(-1)
	})
	for i := 0; i < 20; i++ {
		kick()
		time.Sleep(3 * time.Millisecond)
	}
	<-started
	time.Sleep(250 * time.Millisecond)
	stop()
	if maxRunning.Load() != 1 {
		t.Errorf("renders overlapped: max concurrent = %d", maxRunning.Load())
	}
	if got := runs.Load(); got < 2 || got > 4 {
		t.Errorf("expected coalesced runs (2-4), got %d", got)
	}
	// Nothing runs after stop.
	before := runs.Load()
	kick()
	time.Sleep(30 * time.Millisecond)
	if runs.Load() != before {
		t.Error("refresher ran after stop")
	}
}
