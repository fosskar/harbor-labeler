package labeler

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

type fakeHarbor struct {
	labelID  int64
	projects []string
	labeled  map[string][]ArtifactRef // project -> artifacts carrying the label

	ensuredName string
	added       []ArtifactRef
	removed     []ArtifactRef
	failAdd     map[string]error // ArtifactRef.String() -> error
}

func (f *fakeHarbor) EnsureGlobalLabel(ctx context.Context, name string) (int64, error) {
	f.ensuredName = name
	return f.labelID, nil
}

func (f *fakeHarbor) ListProjects(ctx context.Context) ([]string, error) {
	return f.projects, nil
}

func (f *fakeHarbor) ListLabeledArtifacts(ctx context.Context, project string, labelID int64) ([]ArtifactRef, error) {
	if labelID != f.labelID {
		return nil, fmt.Errorf("unexpected label id %d", labelID)
	}
	return f.labeled[project], nil
}

func (f *fakeHarbor) AddLabel(ctx context.Context, ref ArtifactRef, labelID int64) error {
	if err := f.failAdd[ref.String()]; err != nil {
		return err
	}
	f.added = append(f.added, ref)
	return nil
}

func (f *fakeHarbor) RemoveLabel(ctx context.Context, ref ArtifactRef, labelID int64) error {
	f.removed = append(f.removed, ref)
	return nil
}

const (
	digA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestReconcileAbortsOnZeroImages(t *testing.T) {
	f := &fakeHarbor{labelID: 7}
	err := Reconcile(context.Background(), f, nil, "prod")
	if err == nil {
		t.Fatal("expected error on zero running images")
	}
	if f.ensuredName != "" || f.added != nil || f.removed != nil {
		t.Errorf("harbor was touched despite guard: %+v", f)
	}
}

func TestReconcileLabelsRunningImages(t *testing.T) {
	f := &fakeHarbor{labelID: 7}
	running := []ArtifactRef{
		{Project: "backend", Repository: "api", Digest: digA},
		{Project: "team", Repository: "sub/app", Digest: digB}, // nested repository
	}
	if err := Reconcile(context.Background(), f, running, "prod"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if f.ensuredName != "running-prod" {
		t.Errorf("ensured label %q, want running-prod", f.ensuredName)
	}
	sort.Slice(f.added, func(i, j int) bool { return f.added[i].String() < f.added[j].String() })
	if !reflect.DeepEqual(f.added, running) {
		t.Errorf("added %v, want %v", f.added, running)
	}
}

func TestReconcileRemovesStaleOnly(t *testing.T) {
	stale := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}
	still := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	f := &fakeHarbor{
		labelID:  7,
		projects: []string{"backend", "empty"},
		labeled:  map[string][]ArtifactRef{"backend": {still, stale}},
	}
	running := []ArtifactRef{still}

	if err := Reconcile(context.Background(), f, running, "prod"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.removed) != 1 || f.removed[0] != stale {
		t.Errorf("removed %v, want only %v", f.removed, stale)
	}
}

func TestReconcileContinuesPastAddFailure(t *testing.T) {
	f := &fakeHarbor{
		labelID: 7,
		failAdd: map[string]error{
			"backend/api@" + digA: errors.New("artifact not found"),
		},
	}
	running := []ArtifactRef{
		{Project: "backend", Repository: "api", Digest: digA},
		{Project: "backend", Repository: "worker", Digest: digB},
	}
	err := Reconcile(context.Background(), f, running, "prod")
	if err == nil {
		t.Fatal("expected aggregate error on partial failure")
	}
	if len(f.added) != 1 || f.added[0].Repository != "worker" {
		t.Errorf("added %v, want worker despite api failure", f.added)
	}
}
