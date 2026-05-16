package kube

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestTableToFrame(t *testing.T) {
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{
			{Name: "NAME"}, {Name: "READY"}, {Name: "STATUS"},
		},
		Rows: []metav1.TableRow{
			{Cells: []interface{}{"coredns-1", "1/1", "Running"}},
			{Cells: []interface{}{"coredns-2", "0/1", "Pending"}},
		},
	}
	f := tableToFrame(tbl, false)
	if got, want := f.Headers, []string{"NAME", "READY", "STATUS"}; !equalStr(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}
	if len(f.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(f.Rows))
	}
	if f.Rows[0][0] != "coredns-1" || f.Rows[1][2] != "Pending" {
		t.Errorf("row cells malformed: %v", f.Rows)
	}
}

func TestTableToFrameAllNamespaces(t *testing.T) {
	row1, _ := json.Marshal(map[string]interface{}{
		"metadata": map[string]string{"namespace": "kube-system"},
	})
	tbl := &metav1.Table{
		ColumnDefinitions: []metav1.TableColumnDefinition{{Name: "NAME"}},
		Rows: []metav1.TableRow{
			{Cells: []interface{}{"coredns-1"}, Object: runtime.RawExtension{Raw: row1}},
		},
	}
	f := tableToFrame(tbl, true)
	if got, want := f.Headers, []string{"NAMESPACE", "NAME"}; !equalStr(got, want) {
		t.Errorf("headers = %v, want %v", got, want)
	}
	if got := f.Rows[0]; got[0] != "kube-system" || got[1] != "coredns-1" {
		t.Errorf("row = %v, want [kube-system coredns-1]", got)
	}
}

func TestNamespaceFromRowObject(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte(`{"metadata":{"namespace":"default"}}`), "default"},
		{[]byte(`{"metadata":{}}`), ""},
		{nil, ""},
		{[]byte(`{garbage`), ""},
	}
	for _, tc := range cases {
		if got := namespaceFromRowObject(tc.in); got != tc.want {
			t.Errorf("namespaceFromRowObject(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
