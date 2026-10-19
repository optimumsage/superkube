package guardrail

import "testing"

func TestForbidCommaTypeList(t *testing.T) {
	p := Policy{Forbidden: []string{"delete pods,services"}}
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"pod,service", "web", "--yes"}, true},
		{[]string{"svc,po", "web", "--yes"}, true},
		{[]string{"pods,services"}, true},
		{[]string{"pod", "web"}, false},
		{[]string{"pod,configmap", "web"}, false},
	}
	for _, c := range cases {
		if _, got := p.IsForbidden("delete", c.args); got != c.want {
			t.Errorf("%v: got %v want %v", c.args, got, c.want)
		}
	}
}
