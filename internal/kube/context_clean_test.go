package kube

import (
	"path/filepath"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

// writeFixture lays down a kubeconfig with two contexts: ctx-a uses cluster-a +
// user-a, ctx-b uses cluster-b + user-b. The two contexts share nothing so
// orphan-pruning has obvious correct answers.
func writeFixture(t *testing.T) string {
	t.Helper()
	cfg := api.NewConfig()
	cfg.Clusters["cluster-a"] = &api.Cluster{Server: "https://a.example.invalid:6443"}
	cfg.Clusters["cluster-b"] = &api.Cluster{Server: "https://b.example.invalid:6443"}
	cfg.Clusters["cluster-shared"] = &api.Cluster{Server: "https://shared.example.invalid:6443"}
	cfg.AuthInfos["user-a"] = &api.AuthInfo{Token: "tok-a"}
	cfg.AuthInfos["user-b"] = &api.AuthInfo{Token: "tok-b"}
	cfg.AuthInfos["user-shared"] = &api.AuthInfo{Token: "tok-shared"}
	cfg.Contexts["ctx-a"] = &api.Context{Cluster: "cluster-a", AuthInfo: "user-a"}
	cfg.Contexts["ctx-b"] = &api.Context{Cluster: "cluster-b", AuthInfo: "user-b"}
	cfg.Contexts["ctx-shared-1"] = &api.Context{Cluster: "cluster-shared", AuthInfo: "user-shared"}
	cfg.Contexts["ctx-shared-2"] = &api.Context{Cluster: "cluster-shared", AuthInfo: "user-shared"}
	cfg.CurrentContext = "ctx-a"

	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(*cfg, path); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestDeleteContext_PrunesOrphanedClusterAndAuthInfo(t *testing.T) {
	path := writeFixture(t)
	l := Loader{KubeconfigPath: path}

	if err := l.DeleteContext("ctx-a", true); err != nil {
		t.Fatalf("DeleteContext: %v", err)
	}

	raw, _, err := l.Raw()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := raw.Contexts["ctx-a"]; ok {
		t.Fatal("ctx-a should have been deleted")
	}
	if _, ok := raw.Clusters["cluster-a"]; ok {
		t.Fatal("orphaned cluster-a should have been pruned")
	}
	if _, ok := raw.AuthInfos["user-a"]; ok {
		t.Fatal("orphaned user-a should have been pruned")
	}
	// The unrelated context survives unchanged.
	if _, ok := raw.Contexts["ctx-b"]; !ok {
		t.Fatal("ctx-b should be intact")
	}
	if raw.CurrentContext != "" {
		t.Fatalf("current-context should have been cleared, got %q", raw.CurrentContext)
	}
}

func TestDeleteContext_KeepsSharedClusterAndAuthInfo(t *testing.T) {
	path := writeFixture(t)
	l := Loader{KubeconfigPath: path}

	if err := l.DeleteContext("ctx-shared-1", true); err != nil {
		t.Fatalf("DeleteContext: %v", err)
	}

	raw, _, err := l.Raw()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := raw.Contexts["ctx-shared-1"]; ok {
		t.Fatal("ctx-shared-1 should have been deleted")
	}
	// cluster-shared and user-shared are still referenced by ctx-shared-2, so
	// they must NOT be pruned.
	if _, ok := raw.Clusters["cluster-shared"]; !ok {
		t.Fatal("cluster-shared should remain (still referenced by ctx-shared-2)")
	}
	if _, ok := raw.AuthInfos["user-shared"]; !ok {
		t.Fatal("user-shared should remain (still referenced by ctx-shared-2)")
	}
	// Active context wasn't touched.
	if raw.CurrentContext != "ctx-a" {
		t.Fatalf("current-context should be unchanged, got %q", raw.CurrentContext)
	}
}

func TestDeleteContext_KeepOrphansFlag(t *testing.T) {
	path := writeFixture(t)
	l := Loader{KubeconfigPath: path}

	if err := l.DeleteContext("ctx-a", false); err != nil {
		t.Fatalf("DeleteContext: %v", err)
	}

	raw, _, err := l.Raw()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := raw.Clusters["cluster-a"]; !ok {
		t.Fatal("cluster-a should be retained when pruneOrphans=false")
	}
	if _, ok := raw.AuthInfos["user-a"]; !ok {
		t.Fatal("user-a should be retained when pruneOrphans=false")
	}
}

func TestDeleteContext_UnknownContextErrors(t *testing.T) {
	path := writeFixture(t)
	l := Loader{KubeconfigPath: path}

	if err := l.DeleteContext("does-not-exist", true); err == nil {
		t.Fatal("expected error for unknown context, got nil")
	}
}
