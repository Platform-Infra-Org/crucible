//go:build cluster

package labs

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// TestClusterPSAPolicy: with the production policy rendered, a lab namespace without baseline Pod Security is refused.
func TestClusterPSAPolicy(t *testing.T) {
	kc := os.Getenv("CRUCIBLE_TEST_KUBECONFIG")
	if kc == "" || os.Getenv("CRUCIBLE_TEST_STRICT") != "1" {
		t.Skip("run through make cluster-check")
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "lab-" + newLabID(), Labels: map[string]string{labLabel: "x"}}}
	var err2 error
	for i := 0; i < 30; i++ { // the new policy takes a few seconds to be enforced
		if _, err2 = cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err2 != nil && strings.Contains(err2.Error(), "baseline") {
			return
		}
		_ = cs.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{})
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("a lab namespace without baseline PSA must be refused by admission, got: %v", err2)
}
