package labeler

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// KubeDiscovery discovers running artifacts by listing pods in a Kubernetes
// cluster; it is the ImageDiscovery adapter used in production.
type KubeDiscovery struct {
	client       kubernetes.Interface
	registryHost string
	phases       []corev1.PodPhase
}

// NewKubeDiscovery creates a KubeDiscovery that considers only images hosted
// on registryHost. A non-empty phases list restricts discovery to pods in
// those phases; empty means every pod object counts.
func NewKubeDiscovery(client kubernetes.Interface, registryHost string, phases []corev1.PodPhase) *KubeDiscovery {
	return &KubeDiscovery{client: client, registryHost: registryHost, phases: phases}
}

// RunningImages lists all pods in the cluster and returns the sorted, unique
// artifacts whose container images are hosted on the registry host. Each
// running digest is attributed both to the repository the kubelet reports
// and to the repository the pod spec declares: containerd dedupes pulls by
// digest, so the kubelet may name a different repository holding the same
// digest than the one the workload actually references.
func (d *KubeDiscovery) RunningImages(ctx context.Context) ([]ArtifactRef, error) {
	pods, err := d.client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing pods: %w", err)
	}

	var phaseSet map[corev1.PodPhase]struct{}
	if len(d.phases) > 0 {
		phaseSet = make(map[corev1.PodPhase]struct{}, len(d.phases))
		for _, phase := range d.phases {
			phaseSet[phase] = struct{}{}
		}
	}

	refs := make(map[ArtifactRef]struct{})
	for _, pod := range pods.Items {
		if phaseSet != nil {
			if _, ok := phaseSet[pod.Status.Phase]; !ok {
				continue
			}
		}
		collectImageRefs(refs, pod.Status.ContainerStatuses, pod.Spec.Containers, d.registryHost)
		collectImageRefs(refs, pod.Status.InitContainerStatuses, pod.Spec.InitContainers, d.registryHost)
	}

	images := make([]ArtifactRef, 0, len(refs))
	for ref := range refs {
		images = append(images, ref)
	}
	sort.Slice(images, func(i, j int) bool { return images[i].String() < images[j].String() })
	log.Printf("found %d unique running images from %s", len(images), d.registryHost)
	if len(images) == 0 {
		return nil, nil
	}
	return images, nil
}

// collectImageRefs extracts artifacts hosted on registryHost from container
// statuses into refs. The digest always comes from the kubelet-attested
// imageID; the repository comes from the imageID and, paired by container
// name, from the spec-declared image reference. Refs without a
// project/repository structure cannot exist in Harbor and are skipped.
func collectImageRefs(refs map[ArtifactRef]struct{}, statuses []corev1.ContainerStatus, containers []corev1.Container, registryHost string) {
	specImages := make(map[string]string, len(containers))
	for _, c := range containers {
		specImages[c.Name] = c.Image
	}
	for _, st := range statuses {
		imageID := st.ImageID
		if imageID == "" {
			continue
		}
		// Strip runtime scheme prefixes like "docker-pullable://".
		if idx := strings.Index(imageID, "://"); idx != -1 {
			imageID = imageID[idx+len("://"):]
		}
		path, digest, ok := strings.Cut(imageID, "@")
		if !ok || digest == "" {
			continue
		}

		// the repository the kubelet reports
		if host, rest, ok := strings.Cut(path, "/"); ok && host == registryHost {
			if project, repo, ok := splitProjectRepo(rest); ok {
				refs[ArtifactRef{Project: project, Repository: repo, Digest: digest}] = struct{}{}
			} else {
				log.Printf("skipping image ref %q: no project/repository structure", rest)
			}
		}

		// the repository the pod spec declares, paired with the attested
		// digest
		if specPath, ok := specRepoPath(specImages[st.Name], registryHost); ok {
			if project, repo, ok := splitProjectRepo(specPath); ok {
				refs[ArtifactRef{Project: project, Repository: repo, Digest: digest}] = struct{}{}
			} else {
				log.Printf("skipping spec image ref %q: no project/repository structure", specPath)
			}
		}
	}
}

// splitProjectRepo splits "project/repo[/sub]" into its project and
// repository parts. Paths without a project/repository structure are
// rejected.
func splitProjectRepo(path string) (string, string, bool) {
	project, repo, ok := strings.Cut(path, "/")
	if !ok || project == "" || repo == "" {
		return "", "", false
	}
	return project, repo, true
}

// specRepoPath returns the project/repository path of a spec image
// reference hosted on registryHost, with any tag or digest suffix removed.
// A colon inside the host (a port) is preserved; only a tag after the last
// path separator is stripped.
func specRepoPath(image, registryHost string) (string, bool) {
	image, _, _ = strings.Cut(image, "@")
	if idx := strings.LastIndex(image, ":"); idx > strings.LastIndex(image, "/") {
		image = image[:idx]
	}
	host, rest, ok := strings.Cut(image, "/")
	if !ok || host != registryHost {
		return "", false
	}
	return rest, true
}

// NewKubeClient builds a clientset from the in-cluster service account when
// running inside Kubernetes, falling back to the standard kubeconfig
// resolution (KUBECONFIG, ~/.kube/config) otherwise.
func NewKubeClient() (kubernetes.Interface, error) {
	restCfg, err := rest.InClusterConfig()
	if err != nil {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		restCfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("building Kubernetes client config: %w", err)
		}
	}
	return kubernetes.NewForConfig(restCfg)
}
