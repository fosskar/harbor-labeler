package labeler

import (
	"context"
	"fmt"
	"log"

	"k8s.io/client-go/kubernetes"
)

// Run performs one full reconcile: it discovers the running artifacts in the
// cluster and makes Harbor's "running-<cluster>" label reflect them.
func Run(ctx context.Context, cfg Config, kube kubernetes.Interface, harbor HarborAPI) error {
	images, err := GetRunningImages(ctx, kube, cfg.RegistryHost, cfg.PodPhases)
	if err != nil {
		return fmt.Errorf("discovering running images: %w", err)
	}
	log.Printf("found %d unique running images from %s", len(images), cfg.RegistryHost)

	if err := Reconcile(ctx, harbor, images, cfg.ClusterName); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	log.Printf("reconcile complete for cluster %s", cfg.ClusterName)
	return nil
}
