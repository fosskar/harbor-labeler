package labeler

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// HarborAPI is the Harbor surface Reconcile needs; *Client implements it.
type HarborAPI interface {
	FindGlobalLabel(ctx context.Context, name string) (int64, bool, error)
	EnsureGlobalLabel(ctx context.Context, name string) (int64, error)
	ListAllLabeledArtifacts(ctx context.Context, labelID int64) (LabeledArtifacts, error)
	AddLabel(ctx context.Context, ref ArtifactRef, labelID int64) error
	RemoveLabel(ctx context.Context, ref ArtifactRef, labelID int64) error
}

// plan is the set of label changes one reconcile run intends to make: attach
// the label to these artifacts, detach it from those, and leave the
// alreadyLabeled ones as they are. It is worked out before any write, so a
// dry run reports exactly the requests a normal run applies.
type plan struct {
	labelName      string
	createLabel    bool
	attach         []ArtifactRef
	detach         []ArtifactRef
	alreadyLabeled int
}

// planChanges diffs the running set against the artifacts Harbor reports as
// carrying the label. An incomplete listing cannot prove an absent artifact
// is unlabeled, so it plans an attach for every running artifact; attaching
// is idempotent.
func planChanges(labelName string, running, labeled []ArtifactRef, listingComplete bool) plan {
	labeledSet := make(map[ArtifactRef]struct{}, len(labeled))
	for _, artifact := range labeled {
		labeledSet[artifact] = struct{}{}
	}
	runningSet := make(map[ArtifactRef]struct{}, len(running))
	for _, artifact := range running {
		runningSet[artifact] = struct{}{}
	}

	p := plan{labelName: labelName}
	for _, artifact := range running {
		if _, isLabeled := labeledSet[artifact]; listingComplete && isLabeled {
			p.alreadyLabeled++
			continue
		}
		p.attach = append(p.attach, artifact)
	}
	for _, artifact := range labeled {
		if _, isRunning := runningSet[artifact]; !isRunning {
			p.detach = append(p.detach, artifact)
		}
	}
	return p
}

// report logs the requests the plan describes, sending none of them.
func (p plan) report() {
	if p.createLabel {
		log.Printf("dry-run: would create global label %s", p.labelName)
	}
	for _, artifact := range p.attach {
		log.Printf("dry-run: would label %s with %s", artifact, p.labelName)
	}
	for _, artifact := range p.detach {
		log.Printf("dry-run: would remove %s from %s (no longer running)", p.labelName, artifact)
	}
	log.Printf("dry-run: reconcile complete: would-label=%d already-labeled=%d would-remove=%d",
		len(p.attach), p.alreadyLabeled, len(p.detach))
}

// apply sends the plan's requests. Per-artifact failures are logged and
// aggregated; the remaining requests still go out. listErr carries a failed
// or partial artifact listing into the summary and the joined error, since
// the run reports it but does not stop for it.
func (p plan) apply(ctx context.Context, harbor HarborAPI, labelID int64, sweep LabeledArtifacts, listErr error) error {
	var errs []error
	var labeledCount, skippedMissingProxyCount, failedCount int
	if listErr != nil {
		errs = append(errs, fmt.Errorf("listing labeled artifacts: %w", listErr))
		failedCount++
	}

	for _, artifact := range p.attach {
		if err := harbor.AddLabel(ctx, artifact, labelID); err != nil {
			if errors.Is(err, ErrArtifactNotFound) && sweep.IsProxyCacheProject(artifact.Project) {
				log.Printf("skipped missing proxy-cache artifact %s", artifact)
				skippedMissingProxyCount++
				continue
			}
			if errors.Is(err, ErrArtifactNotFound) && !sweep.ProjectsListed() {
				// decision 12 needs the project list to tell a proxy-cache
				// miss from a deleted artifact; without it, assume the worse
				log.Printf("warning: labeling %s failed, and no project list was available to rule out a proxy-cache miss: %v", artifact, err)
			} else {
				log.Printf("warning: labeling %s failed: %v", artifact, err)
			}
			errs = append(errs, fmt.Errorf("labeling %s: %w", artifact, err))
			failedCount++
			continue
		}
		log.Printf("labeled %s with %s", artifact, p.labelName)
		labeledCount++
	}

	for _, artifact := range p.detach {
		if err := harbor.RemoveLabel(ctx, artifact, labelID); err != nil {
			log.Printf("warning: unlabeling %s failed: %v", artifact, err)
			errs = append(errs, fmt.Errorf("unlabeling %s: %w", artifact, err))
			failedCount++
			continue
		}
		log.Printf("removed %s from %s (no longer running)", p.labelName, artifact)
	}

	log.Printf("reconcile complete: labeled=%d already-labeled=%d skipped-missing-proxy=%d failed=%d",
		labeledCount, p.alreadyLabeled, skippedMissingProxyCount, failedCount)
	return errors.Join(errs...)
}

// Reconcile makes Harbor's "running-<cluster>" label reflect the running set.
// It works out the label changes the cluster needs, then either applies them
// or, in dry-run, only reports them.
func Reconcile(ctx context.Context, harbor HarborAPI, running []ArtifactRef, clusterName string, dryRun bool) error {
	if len(running) == 0 {
		return errors.New("no running images found in cluster; refusing to strip all labels (is pod discovery broken?)")
	}

	labelName := "running-" + clusterName
	var (
		labelID int64
		err     error
	)
	if dryRun {
		var found bool
		labelID, found, err = harbor.FindGlobalLabel(ctx, labelName)
		if err != nil {
			return fmt.Errorf("finding label %q: %w", labelName, err)
		}
		// nothing can carry a label that does not exist yet, so there is
		// nothing to list and every running artifact would be attached
		if !found {
			plan{labelName: labelName, createLabel: true, attach: running}.report()
			return nil
		}
	} else {
		labelID, err = harbor.EnsureGlobalLabel(ctx, labelName)
		if err != nil {
			return fmt.Errorf("ensuring label %q: %w", labelName, err)
		}
	}

	sweep, listErr := harbor.ListAllLabeledArtifacts(ctx, labelID)
	if listErr != nil {
		log.Printf("warning: listing labeled artifacts incomplete: %v", listErr)
	}

	p := planChanges(labelName, running, sweep.Refs, listErr == nil)
	if dryRun {
		p.report()
		if listErr != nil {
			return fmt.Errorf("listing labeled artifacts: %w", listErr)
		}
		return nil
	}
	return p.apply(ctx, harbor, labelID, sweep, listErr)
}
