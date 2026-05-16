package kubectl

import (
	"reflect"
	"testing"
)

func TestPrependGlobalFlags(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		context    string
		namespace  string
		kubeconfig string
		want       []string
	}{
		{
			name: "no overrides",
			in:   []string{"get", "pods"},
			want: []string{"get", "pods"},
		},
		{
			name:      "add namespace when absent",
			in:        []string{"get", "pods"},
			namespace: "kube-system",
			want:      []string{"--namespace", "kube-system", "get", "pods"},
		},
		{
			name:      "respect existing -n",
			in:        []string{"get", "pods", "-n", "default"},
			namespace: "kube-system",
			want:      []string{"get", "pods", "-n", "default"},
		},
		{
			name:      "respect existing --namespace=",
			in:        []string{"--namespace=default", "get", "pods"},
			namespace: "kube-system",
			want:      []string{"--namespace=default", "get", "pods"},
		},
		{
			name:    "add context",
			in:      []string{"get", "pods"},
			context: "prod",
			want:    []string{"--context", "prod", "get", "pods"},
		},
		{
			name:    "respect existing --context=",
			in:      []string{"--context=staging", "get", "pods"},
			context: "prod",
			want:    []string{"--context=staging", "get", "pods"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PrependGlobalFlags(tc.in, tc.context, tc.namespace, tc.kubeconfig)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
