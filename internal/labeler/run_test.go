package labeler

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDiscovery struct {
	images []ArtifactRef
	err    error
}

func (f *fakeDiscovery) RunningImages(ctx context.Context) ([]ArtifactRef, error) {
	return f.images, f.err
}

func TestRunLabelsDiscoveredImages(t *testing.T) {
	ref := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	f := &fakeHarbor{labelID: 7}

	if err := Run(context.Background(), "prod", &fakeDiscovery{images: []ArtifactRef{ref}}, f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.ensuredName != "running-prod" {
		t.Errorf("ensured label %q, want running-prod", f.ensuredName)
	}
	if len(f.added) != 1 || f.added[0] != ref {
		t.Errorf("added %v, want only %v", f.added, ref)
	}
}

func TestRunPropagatesDiscoveryError(t *testing.T) {
	f := &fakeHarbor{labelID: 7}

	err := Run(context.Background(), "prod", &fakeDiscovery{err: errors.New("api server down")}, f)
	if err == nil {
		t.Fatal("expected discovery error to propagate")
	}
	if !strings.Contains(err.Error(), "discovering running images") {
		t.Errorf("error %q missing discovery context", err)
	}
	if f.ensuredName != "" || f.added != nil || f.removed != nil {
		t.Errorf("harbor was touched despite discovery failure: %+v", f)
	}
}

func TestRunPropagatesZeroImageGuard(t *testing.T) {
	f := &fakeHarbor{labelID: 7}

	if err := Run(context.Background(), "prod", &fakeDiscovery{}, f); err == nil {
		t.Fatal("expected error when no running images are discovered")
	}
	if f.ensuredName != "" || f.added != nil || f.removed != nil {
		t.Errorf("harbor was touched despite guard: %+v", f)
	}
}
