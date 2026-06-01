// Package kube provides read-only access to cluster state for the scheduling
// simulator. It deliberately exposes no write surface (no Create/Update/Patch/
// Delete/Bind/Eviction) so the no-impact invariant is enforced at compile time.
package kube

import (
	"fmt"

	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
)

// Clients bundles the read-only clients the tool needs.
type Clients struct {
	Clientset kubernetes.Interface
	Discovery discovery.DiscoveryInterface
}

// NewClients builds clients from genericclioptions.ConfigFlags, honoring the
// --kubeconfig / --context / --namespace flags and the current context.
func NewClients(flags *genericclioptions.ConfigFlags) (*Clients, error) {
	restConfig, err := flags.ToRESTConfig()
	if err != nil {
		return nil, fmt.Errorf("build rest config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	return &Clients{
		Clientset: cs,
		Discovery: cs.Discovery(),
	}, nil
}

// Namespace resolves the effective namespace from the flags (current-context
// namespace unless overridden by --namespace).
func Namespace(flags *genericclioptions.ConfigFlags) (string, error) {
	ns, _, err := flags.ToRawKubeConfigLoader().Namespace()
	if err != nil {
		return "", fmt.Errorf("resolve namespace: %w", err)
	}
	return ns, nil
}
