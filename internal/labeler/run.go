package labeler

import (
	"context"
	"fmt"
	"log"
)

// ImageDiscovery is the discovery surface Run needs; *KubeDiscovery
// implements it.
type ImageDiscovery interface {
	RunningImages(ctx context.Context) ([]ArtifactRef, error)
}

// Run performs one full reconcile: it discovers the running artifacts in the
// cluster and makes Harbor's "running-<cluster>" label reflect them.
func Run(ctx context.Context, clusterName string, discovery ImageDiscovery, harbor HarborAPI) error {
	images, err := discovery.RunningImages(ctx)
	if err != nil {
		return fmt.Errorf("discovering running images: %w", err)
	}

	if err := Reconcile(ctx, harbor, images, clusterName); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	log.Printf("reconcile complete for cluster %s", clusterName)
	return nil
}
