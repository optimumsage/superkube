package helm

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseReleaseSecretName(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantRev  int
	}{
		{"sh.helm.release.v1.nginx.v1", "nginx", 1},
		{"sh.helm.release.v1.my-app.v17", "my-app", 17},
		{"sh.helm.release.v1.release.with.dots.v3", "release.with.dots", 3},
		{"not-a-helm-secret", "", 0},
		{"sh.helm.release.v1.no-revision", "", 0},
		{"sh.helm.release.v1.bad.vXYZ", "", 0},
	}
	for _, c := range cases {
		gotName, gotRev := parseReleaseSecretName(c.in)
		if gotName != c.wantName || gotRev != c.wantRev {
			t.Errorf("parse(%q) = (%q, %d), want (%q, %d)", c.in, gotName, gotRev, c.wantName, c.wantRev)
		}
	}
}

func TestDetectWithClientset_FieldSelectorPath(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("default", "nginx", 1),
		helmSecret("default", "nginx", 2),
		helmSecret("default", "nginx", 3),
		helmSecret("prod", "api", 1),
		// Non-helm secret that should be ignored.
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "random", Namespace: "default"},
			Type:       "Opaque",
		},
	)

	got, err := detectWithClientset(context.Background(), cs, "")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 releases, got %d: %+v", len(got), got)
	}
	// Sort is (ns, name) — so default/nginx then prod/api.
	if got[0].Namespace != "default" || got[0].Name != "nginx" || got[0].Revision != 3 {
		t.Errorf("first release wrong: %+v", got[0])
	}
	if got[1].Namespace != "prod" || got[1].Name != "api" || got[1].Revision != 1 {
		t.Errorf("second release wrong: %+v", got[1])
	}
}

func TestDetectWithClientset_NamespaceScoped(t *testing.T) {
	cs := fake.NewSimpleClientset(
		helmSecret("default", "nginx", 1),
		helmSecret("prod", "api", 1),
	)

	got, err := detectWithClientset(context.Background(), cs, "prod")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if len(got) != 1 || got[0].Namespace != "prod" || got[0].Name != "api" {
		t.Fatalf("want one prod/api release, got %+v", got)
	}
}

// helmSecret constructs a fake Helm 3 release secret. Labels mirror what helm
// actually writes so the label-selector fallback path is also exercised.
func helmSecret(ns, name string, revision int) *corev1.Secret {
	rev := ""
	switch revision {
	case 1:
		rev = "1"
	case 2:
		rev = "2"
	case 3:
		rev = "3"
	case 17:
		rev = "17"
	default:
		rev = "1"
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1." + name + ".v" + rev,
			Namespace: ns,
			Labels: map[string]string{
				"owner":   "helm",
				"name":    name,
				"version": rev,
				"status":  "deployed",
			},
		},
		Type: helmReleaseSecretType,
	}
}
