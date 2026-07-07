package main

import (
	"context"
	"log"

	"github.com/fosskar/harbor-labeler/internal/labeler"
)

func main() {
	cfg, err := labeler.LoadConfig()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	kubeClient, err := labeler.NewKubeClient()
	if err != nil {
		log.Fatalf("kubernetes client: %v", err)
	}

	harborClient, err := labeler.NewClient(cfg.HarborURL, cfg.Username, cfg.Password)
	if err != nil {
		log.Fatalf("harbor client: %v", err)
	}

	ctx := context.Background()

	images, err := labeler.GetRunningImages(ctx, kubeClient, cfg.RegistryHost)
	if err != nil {
		log.Fatalf("discovering running images: %v", err)
	}
	log.Printf("found %d unique running images from %s", len(images), cfg.RegistryHost)

	if err := labeler.Reconcile(ctx, harborClient, images, cfg.ClusterName); err != nil {
		log.Fatalf("reconcile: %v", err)
	}
	log.Printf("reconcile complete for cluster %s", cfg.ClusterName)
}
