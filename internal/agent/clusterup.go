package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"k8s.io/client-go/tools/clientcmd"
	kindcluster "sigs.k8s.io/kind/pkg/cluster"
)

// KindNodeImage is the Kubernetes version `cluster up` makes.
const KindNodeImage = "kindest/node:v1.34.11"

// Kind is the part of kind's provider that cluster up/down use.
type Kind interface {
	List() ([]string, error)
	Create(name, image, kubeconfig string) error
	Delete(name, kubeconfig string) error
}

type dockerKind struct{ p *kindcluster.Provider }

// NewKind drives kind through Docker, quietly.
func NewKind() Kind { return dockerKind{kindcluster.NewProvider(kindcluster.ProviderWithDocker())} }

func (k dockerKind) List() ([]string, error) { return k.p.List() }
func (k dockerKind) Create(name, image, kubeconfig string) error {
	return k.p.Create(name, kindcluster.CreateWithNodeImage(image), kindcluster.CreateWithKubeconfigPath(kubeconfig),
		kindcluster.CreateWithWaitForReady(3*60e9), kindcluster.CreateWithDisplayUsage(false), kindcluster.CreateWithDisplaySalutation(false))
}
func (k dockerKind) Delete(name, kubeconfig string) error { return k.p.Delete(name, kubeconfig) }

// ClusterUp makes the kind cluster `name`, its kubeconfig going to ~/.crucible/kubeconfig only. One that exists stays.
func ClusterUp(k Kind, name string, out io.Writer) error {
	have, err := k.List()
	if err != nil {
		return fmt.Errorf("asking Docker for clusters: %w", err)
	}
	if slices.Contains(have, name) {
		fmt.Fprintf(out, "A cluster called %s already exists on this laptop. Your agent sees it as kind-%s.\n", name, name)
		return nil
	}
	path := crucibleKubeconfig()
	if path == "" {
		return fmt.Errorf("can't find your home directory")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	fmt.Fprintf(out, "Making a Kubernetes cluster called %s on this laptop (needs Docker)…\n", name)
	if err := k.Create(name, KindNodeImage, path); err != nil {
		return fmt.Errorf("making the cluster: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	fmt.Fprintf(out, "Ready. Your agent sees it as kind-%s; pick it under Run it on.\n", name)
	return nil
}

// ClusterDown deletes the cluster and its entries in ~/.crucible/kubeconfig (the file too, once empty).
func ClusterDown(k Kind, name string, out io.Writer) error {
	have, err := k.List()
	if err != nil {
		return fmt.Errorf("asking Docker for clusters: %w", err)
	}
	if !slices.Contains(have, name) {
		fmt.Fprintf(out, "There is no cluster called %s on this laptop.\n", name)
		return nil
	}
	path := crucibleKubeconfig()
	if err := k.Delete(name, path); err != nil {
		return fmt.Errorf("deleting the cluster: %w", err)
	}
	if path != "" {
		if cfg, err := clientcmd.LoadFromFile(path); err == nil {
			key := "kind-" + name
			delete(cfg.Contexts, key)
			delete(cfg.Clusters, key)
			delete(cfg.AuthInfos, key)
			if cfg.CurrentContext == key {
				cfg.CurrentContext = ""
			}
			if len(cfg.Contexts)+len(cfg.Clusters)+len(cfg.AuthInfos) == 0 {
				_ = os.Remove(path)
			} else if err := clientcmd.WriteToFile(*cfg, path); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(out, "Removed the cluster %s from this laptop.\n", name)
	return nil
}
