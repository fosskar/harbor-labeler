package labeler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type fakeHarbor struct {
	labelID    int64
	labeled    []ArtifactRef // artifacts carrying the label, across all projects
	listErr    error         // returned by ListAllLabeledArtifacts alongside labeled
	labelFound bool

	ensuredName string
	added       []ArtifactRef
	removed     []ArtifactRef
	failAdd     map[string]error // ArtifactRef.String() -> error
	// proxyProjects are the projects the sweep classifies as proxy caches.
	proxyProjects map[string]struct{}
	// projectListingFailed makes the sweep report no classification at all,
	// as the real client does when it cannot list Harbor's projects.
	projectListingFailed bool
}

func (f *fakeHarbor) FindGlobalLabel(ctx context.Context, name string) (int64, bool, error) {
	return f.labelID, f.labelFound, nil
}

func (f *fakeHarbor) EnsureGlobalLabel(ctx context.Context, name string) (int64, error) {
	f.ensuredName = name
	return f.labelID, nil
}

func (f *fakeHarbor) ListAllLabeledArtifacts(ctx context.Context, labelID int64) (LabeledArtifacts, error) {
	if labelID != f.labelID {
		return LabeledArtifacts{}, fmt.Errorf("unexpected label id %d", labelID)
	}
	sweep := LabeledArtifacts{Refs: f.labeled}
	if !f.projectListingFailed {
		// a sweep that listed the projects always reports a classification,
		// even when none of them is a proxy cache
		sweep.ProxyProjects = f.proxyProjects
		if sweep.ProxyProjects == nil {
			sweep.ProxyProjects = map[string]struct{}{}
		}
	}
	return sweep, f.listErr
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

func TestPlanChanges(t *testing.T) {
	api := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	worker := ArtifactRef{Project: "backend", Repository: "worker", Digest: digB}
	old := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}

	tests := []struct {
		name            string
		running         []ArtifactRef
		labeled         []ArtifactRef
		listingComplete bool
		want            plan
	}{
		{
			name:            "a running artifact Harbor has not labeled is attached",
			running:         []ArtifactRef{api},
			listingComplete: true,
			want:            plan{attach: []ArtifactRef{api}},
		},
		{
			name:            "an already labeled artifact is counted, not attached again",
			running:         []ArtifactRef{api},
			labeled:         []ArtifactRef{api},
			listingComplete: true,
			want:            plan{alreadyLabeled: 1},
		},
		{
			name:            "a labeled artifact that stopped running is detached",
			running:         []ArtifactRef{api},
			labeled:         []ArtifactRef{api, old},
			listingComplete: true,
			want:            plan{alreadyLabeled: 1, detach: []ArtifactRef{old}},
		},
		{
			name:            "an incomplete listing cannot prove an artifact is labeled, so it is attached anyway",
			running:         []ArtifactRef{api, worker},
			labeled:         []ArtifactRef{api},
			listingComplete: false,
			want:            plan{attach: []ArtifactRef{api, worker}},
		},
		{
			name:            "an incomplete listing still detaches the stale artifacts it did see",
			running:         []ArtifactRef{api},
			labeled:         []ArtifactRef{old},
			listingComplete: false,
			want:            plan{attach: []ArtifactRef{api}, detach: []ArtifactRef{old}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			want.labelName = "running-prod"
			got := planChanges("running-prod", tt.running, tt.labeled, tt.listingComplete)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("planChanges() = %+v, want %+v", got, want)
			}
		})
	}
}

func TestPlanChangesPreservesRunningOrderForAttachment(t *testing.T) {
	first := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	second := ArtifactRef{Project: "backend", Repository: "worker", Digest: digB}

	got := planChanges("running-prod", []ArtifactRef{first, second}, nil, true)
	if !reflect.DeepEqual(got.attach, []ArtifactRef{first, second}) {
		t.Errorf("attach = %v, want the running order %v", got.attach, []ArtifactRef{first, second})
	}
}

func TestReconcileAbortsOnZeroImages(t *testing.T) {
	f := &fakeHarbor{labelID: 7}
	err := Reconcile(context.Background(), f, nil, "prod", false)
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
	if err := Reconcile(context.Background(), f, running, "prod", false); err != nil {
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

func TestReconcileAddsOnlyMissingLabels(t *testing.T) {
	alreadyLabeled := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	missing := ArtifactRef{Project: "backend", Repository: "worker", Digest: digB}
	f := &fakeHarbor{
		labelID: 7,
		labeled: []ArtifactRef{alreadyLabeled},
	}

	if err := Reconcile(context.Background(), f, []ArtifactRef{alreadyLabeled, missing}, "prod", false); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.added) != 1 || f.added[0] != missing {
		t.Errorf("added %v, want only %v", f.added, missing)
	}
	if f.removed != nil {
		t.Errorf("removed %v, want none", f.removed)
	}
}

func TestReconcileRemovesStaleOnly(t *testing.T) {
	stale := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}
	still := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	f := &fakeHarbor{
		labelID: 7,
		labeled: []ArtifactRef{still, stale},
	}
	running := []ArtifactRef{still}

	if err := Reconcile(context.Background(), f, running, "prod", false); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(f.removed) != 1 || f.removed[0] != stale {
		t.Errorf("removed %v, want only %v", f.removed, stale)
	}
	if f.added != nil {
		t.Errorf("added %v, want none", f.added)
	}
}

func TestReconcileFallsBackToAllAddsOnPartialListing(t *testing.T) {
	stale := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}
	alreadyLabeled := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	missing := ArtifactRef{Project: "backend", Repository: "worker", Digest: digB}
	f := &fakeHarbor{
		labelID: 7,
		labeled: []ArtifactRef{alreadyLabeled, stale},
		listErr: errors.New("listing labeled artifacts in team failed"),
	}
	running := []ArtifactRef{alreadyLabeled, missing}

	err := Reconcile(context.Background(), f, running, "prod", false)
	if err == nil {
		t.Fatal("expected aggregate error when listing is incomplete")
	}
	if len(f.removed) != 1 || f.removed[0] != stale {
		t.Errorf("removed %v, want %v despite partial listing", f.removed, stale)
	}
	if !reflect.DeepEqual(f.added, running) {
		t.Errorf("added %v, want all running artifacts %v despite partial listing", f.added, running)
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
	err := Reconcile(context.Background(), f, running, "prod", false)
	if err == nil {
		t.Fatal("expected aggregate error on partial failure")
	}
	if len(f.added) != 1 || f.added[0].Repository != "worker" {
		t.Errorf("added %v, want worker despite api failure", f.added)
	}
}

func TestReconcileMissingArtifactDependsOnProjectType(t *testing.T) {
	ref := ArtifactRef{Project: "docker-hub", Repository: "aquasec/trivy-operator", Digest: digA}
	existing := ArtifactRef{Project: "docker-hub", Repository: "library/alpine", Digest: digB}

	t.Run("missing proxy-cache artifact is skipped", func(t *testing.T) {
		logs := captureLogs(t)

		f := &fakeHarbor{
			labelID:       7,
			proxyProjects: map[string]struct{}{"docker-hub": {}},
			failAdd: map[string]error{
				ref.String(): fmt.Errorf("adding label: %w", ErrArtifactNotFound),
			},
		}

		if err := Reconcile(context.Background(), f, []ArtifactRef{ref, existing}, "prod", false); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if len(f.added) != 1 || f.added[0] != existing {
			t.Errorf("added %v, want existing proxy artifact %v", f.added, existing)
		}
		if !strings.Contains(logs.String(), "skipped missing proxy-cache artifact "+ref.String()) {
			t.Errorf("logs missing proxy-cache skip: %s", logs.String())
		}
		if !strings.Contains(logs.String(), "reconcile complete: labeled=1 already-labeled=0 skipped-missing-proxy=1 failed=0") {
			t.Errorf("logs missing reconciliation summary: %s", logs.String())
		}
	})

	t.Run("missing normal-project artifact remains an error", func(t *testing.T) {
		f := &fakeHarbor{
			labelID: 7,
			failAdd: map[string]error{
				ref.String(): fmt.Errorf("adding label: %w", ErrArtifactNotFound),
			},
		}

		err := Reconcile(context.Background(), f, []ArtifactRef{ref}, "prod", false)
		if !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("Reconcile error = %v, want ErrArtifactNotFound", err)
		}
	})

	t.Run("without a project list the miss cannot be ruled out and stays an error", func(t *testing.T) {
		logs := captureLogs(t)
		f := &fakeHarbor{
			labelID:              7,
			proxyProjects:        map[string]struct{}{"docker-hub": {}},
			projectListingFailed: true,
			listErr:              errors.New("listing projects: boom"),
			failAdd: map[string]error{
				ref.String(): fmt.Errorf("adding label: %w", ErrArtifactNotFound),
			},
		}

		err := Reconcile(context.Background(), f, []ArtifactRef{ref}, "prod", false)
		if !errors.Is(err, ErrArtifactNotFound) {
			t.Fatalf("Reconcile error = %v, want ErrArtifactNotFound", err)
		}
		if strings.Contains(logs.String(), "skipped missing proxy-cache artifact") {
			t.Errorf("an unclassifiable artifact must not be skipped: %s", logs.String())
		}
		if !strings.Contains(logs.String(), "no project list was available to rule out a proxy-cache miss") {
			t.Errorf("logs must say why the miss could not be ruled out: %s", logs.String())
		}
	})
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	originalOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(originalOutput) })
	return &logs
}

func TestReconcileDryRunReportsChangesWithoutWriting(t *testing.T) {
	alreadyLabeled := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	missing := ArtifactRef{Project: "backend", Repository: "worker", Digest: digB}
	stale := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}
	f := &fakeHarbor{
		labelID:    7,
		labelFound: true,
		labeled:    []ArtifactRef{alreadyLabeled, stale},
	}
	logs := captureLogs(t)

	if err := Reconcile(context.Background(), f, []ArtifactRef{alreadyLabeled, missing}, "prod", true); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if f.ensuredName != "" || f.added != nil || f.removed != nil {
		t.Errorf("harbor was mutated in dry-run: %+v", f)
	}
	if !strings.Contains(logs.String(), "dry-run: would label "+missing.String()+" with running-prod") {
		t.Errorf("logs %q missing planned label", logs.String())
	}
	if !strings.Contains(logs.String(), "dry-run: would remove running-prod from "+stale.String()) {
		t.Errorf("logs %q missing planned removal", logs.String())
	}
	if !strings.Contains(logs.String(), "dry-run: reconcile complete: would-label=1 already-labeled=1 would-remove=1") {
		t.Errorf("logs %q missing dry-run summary of the planned changes", logs.String())
	}
}

func TestReconcileDryRunReportsMissingLabelWithoutWriting(t *testing.T) {
	running := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	f := &fakeHarbor{labelID: 7}
	logs := captureLogs(t)

	if err := Reconcile(context.Background(), f, []ArtifactRef{running}, "prod", true); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if f.ensuredName != "" || f.added != nil || f.removed != nil {
		t.Errorf("harbor was mutated in dry-run: %+v", f)
	}
	if !strings.Contains(logs.String(), "dry-run: would create global label running-prod") {
		t.Errorf("logs %q missing planned label creation", logs.String())
	}
	if !strings.Contains(logs.String(), "dry-run: would label "+running.String()+" with running-prod") {
		t.Errorf("logs %q missing planned attachment", logs.String())
	}
}

func TestReconcileDryRunMirrorsPartialListingAndReturnsError(t *testing.T) {
	running := ArtifactRef{Project: "backend", Repository: "api", Digest: digA}
	stale := ArtifactRef{Project: "backend", Repository: "old", Digest: digB}
	f := &fakeHarbor{
		labelID:    7,
		labelFound: true,
		labeled:    []ArtifactRef{running, stale},
		listErr:    errors.New("listing failed"),
	}
	logs := captureLogs(t)

	if err := Reconcile(context.Background(), f, []ArtifactRef{running}, "prod", true); err == nil {
		t.Fatal("expected partial listing error")
	}
	if f.added != nil || f.removed != nil {
		t.Errorf("harbor was mutated in dry-run: %+v", f)
	}
	if !strings.Contains(logs.String(), "dry-run: would label "+running.String()+" with running-prod") {
		t.Errorf("logs %q missing mirrored attachment", logs.String())
	}
	if !strings.Contains(logs.String(), "dry-run: would remove running-prod from "+stale.String()) {
		t.Errorf("logs %q missing known stale removal", logs.String())
	}
}
