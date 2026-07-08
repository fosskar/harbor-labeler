package labeler

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// HarborAPI is the Harbor surface Reconcile needs; *Client implements it.
type HarborAPI interface {
	EnsureGlobalLabel(ctx context.Context, name string) (int64, error)
	ListProjects(ctx context.Context) ([]string, error)
	ListLabeledArtifacts(ctx context.Context, project string, labelID int64) ([]ArtifactRef, error)
	AddLabel(ctx context.Context, ref ArtifactRef, labelID int64) error
	RemoveLabel(ctx context.Context, ref ArtifactRef, labelID int64) error
}

// Reconcile makes Harbor's "running-<cluster>" label reflect the running set:
// it attaches the label to every running artifact and detaches it from every
// labeled artifact that is no longer running. Per-artifact failures are
// logged and aggregated; the rest of the run continues.
func Reconcile(ctx context.Context, harbor HarborAPI, running []ArtifactRef, clusterName string) error {
	if len(running) == 0 {
		return errors.New("no running images found in cluster; refusing to strip all labels (is pod discovery broken?)")
	}

	labelName := "running-" + clusterName
	labelID, err := harbor.EnsureGlobalLabel(ctx, labelName)
	if err != nil {
		return fmt.Errorf("ensuring label %q: %w", labelName, err)
	}

	var errs []error
	runningSet := make(map[ArtifactRef]struct{}, len(running))

	// Attach the label to all running artifacts.
	for _, artifact := range running {
		runningSet[artifact] = struct{}{}
		if err := harbor.AddLabel(ctx, artifact, labelID); err != nil {
			log.Printf("warning: labeling %s failed: %v", artifact, err)
			errs = append(errs, fmt.Errorf("labeling %s: %w", artifact, err))
			continue
		}
		log.Printf("labeled %s with %s", artifact, labelName)
	}

	// Detach the label from artifacts that are no longer running.
	projects, err := harbor.ListProjects(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing projects: %w", err))
		return errors.Join(errs...)
	}
	for _, project := range projects {
		labeled, err := harbor.ListLabeledArtifacts(ctx, project, labelID)
		if err != nil {
			log.Printf("warning: listing labeled artifacts in %s failed: %v", project, err)
			errs = append(errs, fmt.Errorf("listing labeled artifacts in %s: %w", project, err))
			continue
		}
		for _, artifact := range labeled {
			if _, isRunning := runningSet[artifact]; isRunning {
				continue
			}
			if err := harbor.RemoveLabel(ctx, artifact, labelID); err != nil {
				log.Printf("warning: unlabeling %s failed: %v", artifact, err)
				errs = append(errs, fmt.Errorf("unlabeling %s: %w", artifact, err))
				continue
			}
			log.Printf("removed %s from %s (no longer running)", labelName, artifact)
		}
	}

	return errors.Join(errs...)
}
