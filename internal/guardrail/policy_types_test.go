package guardrail

import "testing"

func TestForbidNormalizesTypePositions(t *testing.T) {
	cases := []struct {
		rule string
		args []string
		want bool
	}{
		{"delete leases", []string{"leases.coordination.k8s.io", "leader", "--yes"}, true},
		{"delete leases", []string{"lease/leader"}, true},
		{"delete leases", []string{"leases.v1.coordination.k8s.io/leader"}, true},
		{"delete endpointslices", []string{"endpointslices.discovery.k8s.io", "x"}, true},
		{"delete widgets", []string{"widgets.example.com", "w1"}, true},
		{"delete widgets", []string{"widgets.example.com/w1"}, true},
		{"delete widgets", []string{"widget", "w1"}, true},
		{"delete widgets", []string{"gadgets.example.com", "w1"}, false},
		{"delete pod web.prod", []string{"pod/web.prod"}, true},
		{"delete pod web.prod", []string{"pod/web.staging"}, false},
		{"delete pod web.prod", []string{"pods", "web.staging"}, false},
		{"delete pod web.prod", []string{"pods", "web.prod"}, true},
	}
	for _, c := range cases {
		p := Policy{Forbidden: []string{c.rule}}
		if _, got := p.IsForbidden("delete", c.args); got != c.want {
			t.Errorf("%q vs %v: got %v want %v", c.rule, c.args, got, c.want)
		}
	}
}
