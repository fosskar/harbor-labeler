//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const namespaceName = "harbor-labeler-e2e"

type artifact struct {
	Digest string `json:"digest"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// harborArtifacts queries the Harbor v2 API directly so the test verifies the
// labeler through a seam independent of internal/labeler.
func harborArtifacts(t *testing.T, repo string) []artifact {
	t.Helper()

	endpoint := fmt.Sprintf(
		"%s/api/v2.0/projects/e2e/repositories/%s/artifacts?with_label=true",
		strings.TrimSuffix(os.Getenv("HARBOR_URL"), "/"),
		url.PathEscape(repo),
	)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("building harbor request: %v", err)
	}
	req.SetBasicAuth(os.Getenv("HARBOR_USERNAME"), os.Getenv("HARBOR_PASSWORD"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("querying harbor artifacts for %s: %v", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("harbor artifacts for %s: unexpected status %d", repo, resp.StatusCode)
	}

	var artifacts []artifact
	if err := json.NewDecoder(resp.Body).Decode(&artifacts); err != nil {
		t.Fatalf("decoding harbor artifacts for %s: %v", repo, err)
	}
	return artifacts
}

func hasLabel(artifacts []artifact, digest, labelName string) bool {
	for _, a := range artifacts {
		if a.Digest != digest {
			continue
		}
		for _, l := range a.Labels {
			if l.Name == labelName {
				return true
			}
		}
	}
	return false
}

func runLabeler(t *testing.T) (int, string) {
	t.Helper()

	cmd := exec.Command(os.Getenv("LABELER_BIN"))
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running labeler: %v\noutput:\n%s", err, out)
		}
		return exitErr.ExitCode(), string(out)
	}
	return 0, string(out)
}

// imageDigest extracts the digest from a container status imageID like
// localhost:30002/e2e/app-a@sha256:abc...; harbor reports the same digest,
// which is exactly the contract this e2e test pins.
func imageDigest(t *testing.T, imageID string) string {
	t.Helper()
	_, digest, found := strings.Cut(imageID, "@")
	if !found {
		t.Fatalf("imageID %q has no digest part", imageID)
	}
	return digest
}

func makePod(name, image string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "app",
				Image: image,
				// busybox exits immediately without an explicit command
				Command: []string{"sleep", "3600"},
			}},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// waitPodRunningWithImageID waits until the pod is running and the kubelet has
// reported a resolved imageID, which is what the labeler consumes.
func waitPodRunningWithImageID(ctx context.Context, client kubernetes.Interface, name string) (string, error) {
	var imageID string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			pod, err := client.CoreV1().Pods(namespaceName).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if pod.Status.Phase != corev1.PodRunning || len(pod.Status.ContainerStatuses) == 0 {
				return false, nil
			}
			imageID = pod.Status.ContainerStatuses[0].ImageID
			return imageID != "", nil
		})
	return imageID, err
}

func deletePodAndWaitGone(ctx context.Context, client kubernetes.Interface, name string) error {
	zero := int64(0)
	err := client.CoreV1().Pods(namespaceName).Delete(ctx, name, metav1.DeleteOptions{
		GracePeriodSeconds: &zero,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return wait.PollUntilContextTimeout(ctx, time.Second, time.Minute, true,
		func(ctx context.Context) (bool, error) {
			_, err := client.CoreV1().Pods(namespaceName).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		})
}

// pollHasLabel re-queries harbor until the label state matches; harbor's
// labeled-artifact view can lag slightly behind label mutations.
func pollHasLabel(t *testing.T, repo, digest, labelName string, want bool) bool {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := hasLabel(harborArtifacts(t, repo), digest, labelName)
		if got == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(2 * time.Second)
	}
}

func TestReconcileEndToEnd(t *testing.T) {
	if os.Getenv("LABELER_BIN") == "" {
		t.Skip("LABELER_BIN not set; e2e infrastructure not provisioned (see e2e/run.sh)")
	}

	label := "running-" + os.Getenv("CLUSTER_NAME")
	ctx := context.Background()

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		t.Fatalf("loading kubeconfig: %v", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("building clientset: %v", err)
	}

	var digestA, digestB string

	// namespace lifecycle lives on the parent test: a t.Cleanup inside the
	// setup subtest would fire as soon as that subtest returns, tearing the
	// pods down before the later subtests run.
	if _, err := client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = client.CoreV1().Namespaces().Delete(context.Background(), namespaceName, metav1.DeleteOptions{})
	})

	ok := t.Run("setup", func(t *testing.T) {

		for name, image := range map[string]string{
			"pod-a": os.Getenv("E2E_IMAGE_A"),
			"pod-b": os.Getenv("E2E_IMAGE_B"),
		} {
			if _, err := client.CoreV1().Pods(namespaceName).Create(ctx, makePod(name, image), metav1.CreateOptions{}); err != nil {
				t.Fatalf("creating %s: %v", name, err)
			}
		}

		imageIDA, err := waitPodRunningWithImageID(ctx, client, "pod-a")
		if err != nil {
			t.Fatalf("waiting for pod-a: %v", err)
		}
		imageIDB, err := waitPodRunningWithImageID(ctx, client, "pod-b")
		if err != nil {
			t.Fatalf("waiting for pod-b: %v", err)
		}
		digestA = imageDigest(t, imageIDA)
		digestB = imageDigest(t, imageIDB)
		t.Logf("pod-a digest %s, pod-b digest %s", digestA, digestB)
	})
	if !ok {
		t.Fatal("setup failed")
	}

	ok = t.Run("attach", func(t *testing.T) {
		exitCode, out := runLabeler(t)
		t.Logf("labeler output:\n%s", out)
		if exitCode != 0 {
			t.Fatalf("labeler exited %d, want 0", exitCode)
		}
		if got := pollHasLabel(t, "app-a", digestA, label, true); !got {
			t.Errorf("app-a artifact %s missing label %s", digestA, label)
		}
		if got := pollHasLabel(t, "app-b", digestB, label, true); !got {
			t.Errorf("app-b artifact %s missing label %s", digestB, label)
		}
	})
	if !ok {
		t.Fatal("attach failed")
	}

	ok = t.Run("detach", func(t *testing.T) {
		if err := deletePodAndWaitGone(ctx, client, "pod-a"); err != nil {
			t.Fatalf("deleting pod-a: %v", err)
		}

		exitCode, out := runLabeler(t)
		t.Logf("labeler output:\n%s", out)
		if exitCode != 0 {
			t.Fatalf("labeler exited %d, want 0", exitCode)
		}
		if got := pollHasLabel(t, "app-a", digestA, label, false); got {
			t.Errorf("app-a artifact %s still carries label %s after pod deletion", digestA, label)
		}
		if got := pollHasLabel(t, "app-b", digestB, label, true); !got {
			t.Errorf("app-b artifact %s lost label %s while its pod still runs", digestB, label)
		}
	})
	if !ok {
		t.Fatal("detach failed")
	}

	ok = t.Run("safety guard", func(t *testing.T) {
		if err := deletePodAndWaitGone(ctx, client, "pod-b"); err != nil {
			t.Fatalf("deleting pod-b: %v", err)
		}

		exitCode, out := runLabeler(t)
		t.Logf("labeler output:\n%s", out)
		if exitCode == 0 {
			t.Fatal("labeler exited 0 with no running images; safety guard should force non-zero exit")
		}
		// the guard must leave harbor untouched, so app-b keeps its label
		if got := pollHasLabel(t, "app-b", digestB, label, true); !got {
			t.Errorf("app-b artifact %s lost label %s; guard stripped labels despite zero running images", digestB, label)
		}
	})
	if !ok {
		t.Fatal("safety guard failed")
	}

	t.Run("chart run", func(t *testing.T) {
		cronName := os.Getenv("E2E_CRONJOB")
		if cronName == "" {
			t.Skip("E2E_CRONJOB not set; chart deployment not provisioned (see e2e/run.sh)")
		}
		cronNS := os.Getenv("E2E_CRONJOB_NAMESPACE")

		if _, err := client.CoreV1().Pods(namespaceName).Create(ctx, makePod("pod-a", os.Getenv("E2E_IMAGE_A")), metav1.CreateOptions{}); err != nil {
			t.Fatalf("recreating pod-a: %v", err)
		}
		t.Cleanup(func() {
			_ = deletePodAndWaitGone(context.Background(), client, "pod-a")
		})
		imageIDA, err := waitPodRunningWithImageID(ctx, client, "pod-a")
		if err != nil {
			t.Fatalf("waiting for pod-a: %v", err)
		}
		// the image may have been re-pushed since setup, so the digest from
		// the earlier subtests cannot be trusted here
		digestA = imageDigest(t, imageIDA)

		cron, err := client.BatchV1().CronJobs(cronNS).Get(ctx, cronName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("fetching cronjob %s/%s: %v", cronNS, cronName, err)
		}

		// mirror `kubectl create job --from=cronjob/...`: spec verbatim, and
		// template labels copied so the chart's networkpolicy podSelector
		// still matches the job pod
		const jobName = "e2e-chart-run"
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:   jobName,
				Labels: cron.Spec.JobTemplate.Labels,
			},
			Spec: cron.Spec.JobTemplate.Spec,
		}
		if _, err := client.BatchV1().Jobs(cronNS).Create(ctx, job, metav1.CreateOptions{}); err != nil {
			t.Fatalf("creating job %s: %v", jobName, err)
		}
		t.Cleanup(func() {
			propagation := metav1.DeletePropagationBackground
			_ = client.BatchV1().Jobs(cronNS).Delete(context.Background(), jobName, metav1.DeleteOptions{
				PropagationPolicy: &propagation,
			})
		})

		// the job pod's logs are the only visibility into the in-cluster run
		jobLogs := func() string {
			pods, err := client.CoreV1().Pods(cronNS).List(ctx, metav1.ListOptions{
				LabelSelector: "job-name=" + jobName,
			})
			if err != nil {
				return fmt.Sprintf("(listing job pods: %v)", err)
			}
			if len(pods.Items) == 0 {
				return "(no job pods found)"
			}
			var sb strings.Builder
			for _, p := range pods.Items {
				raw, err := client.CoreV1().Pods(cronNS).GetLogs(p.Name, &corev1.PodLogOptions{}).Do(ctx).Raw()
				if err != nil {
					fmt.Fprintf(&sb, "--- %s ---\n(logs unavailable: %v)\n", p.Name, err)
					continue
				}
				fmt.Fprintf(&sb, "--- %s ---\n%s", p.Name, raw)
			}
			return sb.String()
		}

		err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 3*time.Minute, true,
			func(ctx context.Context) (bool, error) {
				j, err := client.BatchV1().Jobs(cronNS).Get(ctx, jobName, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				for _, c := range j.Status.Conditions {
					if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
						return false, fmt.Errorf("job failed: %s: %s", c.Reason, c.Message)
					}
				}
				return j.Status.Succeeded >= 1, nil
			})
		if err != nil {
			t.Fatalf("waiting for job %s: %v\nlogs:\n%s", jobName, err, jobLogs())
		}
		t.Logf("in-cluster labeler logs:\n%s", jobLogs())

		if got := pollHasLabel(t, "app-a", digestA, label, true); !got {
			t.Errorf("app-a artifact %s missing label %s after in-cluster run", digestA, label)
		}
		// the guard stage left app-b's label behind; the in-cluster run sees
		// pod-a only, so it must detach the stale app-b label
		if got := pollHasLabel(t, "app-b", digestB, label, false); got {
			t.Errorf("app-b artifact %s still carries label %s after in-cluster run", digestB, label)
		}
	})
}
