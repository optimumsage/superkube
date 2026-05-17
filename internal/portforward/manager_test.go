package portforward

import "testing"

func TestBuildKubectlArgs(t *testing.T) {
	cases := []struct {
		name string
		opts StartOpts
		want []string
	}{
		{
			name: "minimal",
			opts: StartOpts{Target: "pod/foo", Ports: []string{"8080:80"}},
			want: []string{"port-forward", "pod/foo", "8080:80"},
		},
		{
			name: "with namespace and context",
			opts: StartOpts{
				Target:    "svc/web",
				Ports:     []string{"9090:9090"},
				Namespace: "kube-system",
				Context:   "prod",
			},
			want: []string{"port-forward", "--context", "prod", "-n", "kube-system", "svc/web", "9090:9090"},
		},
		{
			name: "with kubeconfig and address",
			opts: StartOpts{
				Target:     "deploy/api",
				Ports:      []string{"5005:5005"},
				Kubeconfig: "/tmp/kc",
				Address:    "0.0.0.0",
			},
			want: []string{"port-forward", "--kubeconfig", "/tmp/kc", "--address", "0.0.0.0", "deploy/api", "5005:5005"},
		},
		{
			name: "multiple ports",
			opts: StartOpts{Target: "pod/x", Ports: []string{"80:80", "443:443"}},
			want: []string{"port-forward", "pod/x", "80:80", "443:443"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildKubectlArgs(tc.opts)
			if !equalStrings(got, tc.want) {
				t.Errorf("buildKubectlArgs: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAliveSelf(t *testing.T) {
	if !isAlive(selfPID()) {
		t.Error("expected own PID to be alive")
	}
	if isAlive(0) {
		t.Error("PID 0 should never be considered alive")
	}
	if isAlive(99999999) {
		t.Error("absurd PID should not be alive (assuming no such process)")
	}
}

func equalStrings(a, b []string) bool {
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
