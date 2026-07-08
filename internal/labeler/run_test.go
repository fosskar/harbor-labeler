package labeler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRunLabelsDiscoveredImages(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("api-1", "default", []corev1.ContainerStatus{
			status("harbor.example.com/backend/api@" + digA),
		}, nil),
	)
	f := &fakeHarbor{labelID: 7}
	cfg := Config{RegistryHost: "harbor.example.com", ClusterName: "prod"}

	if err := Run(context.Background(), cfg, client, f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.ensuredName != "running-prod" {
		t.Errorf("ensured label %q, want running-prod", f.ensuredName)
	}
	want := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	if len(f.added) != 1 || f.added[0] != want {
		t.Errorf("added %v, want only %v", f.added, want)
	}
}

func TestRunPropagatesZeroImageGuard(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("other-1", "default", []corev1.ContainerStatus{
			status("docker.io/library/nginx@" + digB), // wrong registry
		}, nil),
	)
	f := &fakeHarbor{labelID: 7}
	cfg := Config{RegistryHost: "harbor.example.com", ClusterName: "prod"}

	if err := Run(context.Background(), cfg, client, f); err == nil {
		t.Fatal("expected error when no running images are discovered")
	}
	if f.ensuredName != "" || f.added != nil || f.removed != nil {
		t.Errorf("harbor was touched despite guard: %+v", f)
	}
}

func TestRunRemovesStaleArtifacts(t *testing.T) {
	client := fake.NewSimpleClientset(
		pod("api-1", "default", []corev1.ContainerStatus{
			status("harbor.example.com/backend/api@" + digA),
		}, nil),
	)
	still := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	stale := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}
	f := &fakeHarbor{
		labelID: 7,
		labeled: []ArtifactRef{still, stale},
	}
	cfg := Config{RegistryHost: "harbor.example.com", ClusterName: "prod"}

	if err := Run(context.Background(), cfg, client, f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.removed) != 1 || f.removed[0] != stale {
		t.Errorf("removed %v, want only %v", f.removed, stale)
	}
}
