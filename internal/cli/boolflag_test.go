package cli

import "testing"

func TestBoolFlag(t *testing.T) {
	cases := []struct {
		args    []string
		want    bool
		wantErr bool
	}{
		{[]string{"-f"}, false, false},
		{[]string{"--all-containers"}, true, false},
		{[]string{"--all-containers=true"}, true, false},
		{[]string{"--all-containers=false"}, false, false},
		{[]string{"--all-containers=0"}, false, false},
		{[]string{"--all-containers=maybe"}, false, true},
		{[]string{"--all-containers=true", "--all-containers=false"}, false, false}, // last wins
		{[]string{"--all-containers=false", "--all-containers"}, true, false},
		{[]string{"--all-containers=maybe", "--all-containers=true"}, false, true}, // every occurrence validated
	}
	for _, c := range cases {
		got, err := strictBoolFlag(c.args, "--all-containers")
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("strictBoolFlag(%v) = %v, %v; want %v (err=%v)", c.args, got, err, c.want, c.wantErr)
		}
	}
}
