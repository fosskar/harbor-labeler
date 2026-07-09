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

	discovery := labeler.NewKubeDiscovery(kubeClient, cfg.RegistryHost, cfg.PodPhases)
	if err := labeler.Run(context.Background(), cfg.ClusterName, discovery, harborClient); err != nil {
		log.Fatal(err)
	}
}
