package kube

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Sentinel errors so callers can distinguish user-input errors (not-found) from
// degraded conditions (RBAC) without string matching.
var (
	// ErrPodNotFound / ErrNodeNotFound indicate user input that does not exist.
	ErrPodNotFound  = errors.New("pod not found")
	ErrNodeNotFound = errors.New("node not found")
	// ErrForbidden indicates the caller lacks RBAC for an otherwise-valid read.
	ErrForbidden = errors.New("forbidden (RBAC)")
)

// listPageLimit bounds each page of the full List calls as a defense against a
// single huge response (memory spike / timeout). With RV="0" the watch cache
// may ignore Limit (version-dependent), so pagination is "best effort" and the
// context timeout is the real safety net (design §3.2.1).
const listPageLimit int64 = 500

// ClusterReader provides the read-only cluster state needed for simulation.
// Write-style methods are intentionally absent (compile-time no-impact
// guarantee). The consumer (cli orchestration) can swap in a fake.
type ClusterReader interface {
	GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error)
	GetNode(ctx context.Context, name string) (*corev1.Node, error)

	// ListAllNodes returns every node in the cluster (RV=0 + pagination). Used
	// to build the full cluster snapshot — PodTopologySpread / InterPodAffinity
	// topology (design §3.1 #4).
	ListAllNodes(ctx context.Context) ([]*corev1.Node, error)
	// ListAllPods returns every pod across all namespaces (RV=0 + pagination).
	// Used to place pods on every NodeInfo and to index affinity / anti-affinity
	// (design §3.1 #5). Intentional full List — accuracy over cost (§3).
	ListAllPods(ctx context.Context) ([]*corev1.Pod, error)

	// --- Volume-filter (design §2.3 ②/③) read-only reads (Round 5). All Get/List
	// only (no write surface). These objects are fed to a fake clientset informer
	// factory (§2.6); the volume plugins read them via the standard listers. ---

	// ListCSINodes returns every CSINode (node volume limits / driver topology).
	ListCSINodes(ctx context.Context) ([]*storagev1.CSINode, error)
	// ListStorageClasses returns every StorageClass (volumeBindingMode static/
	// dynamic gating + zone topology + default-SC resolution).
	ListStorageClasses(ctx context.Context) ([]*storagev1.StorageClass, error)
	// GetPVC returns one PVC referenced by the target pod (binding state via
	// Spec.VolumeName, requested StorageClass).
	GetPVC(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error)
	// GetPV returns the PV bound to a PVC (zone labels / node-affinity topology).
	GetPV(ctx context.Context, name string) (*corev1.PersistentVolume, error)
}

// reader is the client-go backed ClusterReader. It uses only Get/List.
type reader struct {
	clientset kubernetes.Interface
}

var _ ClusterReader = (*reader)(nil)

// NewReader returns a read-only ClusterReader over the given clientset.
func NewReader(clientset kubernetes.Interface) ClusterReader {
	return &reader{clientset: clientset}
}

func (r *reader) GetPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	pod, err := r.clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", namespace, name, classifyGetErr(err, ErrPodNotFound))
	}
	return pod, nil
}

func (r *reader) GetNode(ctx context.Context, name string) (*corev1.Node, error) {
	node, err := r.clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get node %s: %w", name, classifyGetErr(err, ErrNodeNotFound))
	}
	return node, nil
}

func (r *reader) GetPVC(ctx context.Context, namespace, name string) (*corev1.PersistentVolumeClaim, error) {
	pvc, err := r.clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pvc %s/%s: %w", namespace, name, classifyGetErr(err, ErrPodNotFound))
	}
	return pvc, nil
}

func (r *reader) GetPV(ctx context.Context, name string) (*corev1.PersistentVolume, error) {
	pv, err := r.clientset.CoreV1().PersistentVolumes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pv %s: %w", name, classifyGetErr(err, ErrPodNotFound))
	}
	return pv, nil
}

// ListAllNodes lists every node with the same RV=0 + pagination + timeout +
// RBAC-degrade defenses as the pod list (design §3.2.1).
func (r *reader) ListAllNodes(ctx context.Context) ([]*corev1.Node, error) {
	var out []*corev1.Node
	err := r.paginate(ctx, "nodes", func(opts metav1.ListOptions) (string, error) {
		list, lerr := r.clientset.CoreV1().Nodes().List(ctx, opts)
		if lerr != nil {
			return "", lerr
		}
		for i := range list.Items {
			out = append(out, &list.Items[i])
		}
		return list.Continue, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListAllPods lists every pod across all namespaces with the same defenses.
func (r *reader) ListAllPods(ctx context.Context) ([]*corev1.Pod, error) {
	var out []*corev1.Pod
	err := r.paginate(ctx, "pods", func(opts metav1.ListOptions) (string, error) {
		list, lerr := r.clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx, opts)
		if lerr != nil {
			return "", lerr
		}
		for i := range list.Items {
			out = append(out, &list.Items[i])
		}
		return list.Continue, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *reader) ListCSINodes(ctx context.Context) ([]*storagev1.CSINode, error) {
	var out []*storagev1.CSINode
	err := r.paginate(ctx, "csinodes", func(opts metav1.ListOptions) (string, error) {
		list, lerr := r.clientset.StorageV1().CSINodes().List(ctx, opts)
		if lerr != nil {
			return "", lerr
		}
		for i := range list.Items {
			out = append(out, &list.Items[i])
		}
		return list.Continue, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *reader) ListStorageClasses(ctx context.Context) ([]*storagev1.StorageClass, error) {
	var out []*storagev1.StorageClass
	err := r.paginate(ctx, "storageclasses", func(opts metav1.ListOptions) (string, error) {
		list, lerr := r.clientset.StorageV1().StorageClasses().List(ctx, opts)
		if lerr != nil {
			return "", lerr
		}
		for i := range list.Items {
			out = append(out, &list.Items[i])
		}
		return list.Continue, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// paginate runs the given List page function with RV="0" + Limit + continue.
// resource is used only for the wrapped error message. A Forbidden response is
// surfaced as ErrForbidden so the caller can degrade the dependent filters to
// SKIPPED rather than failing the whole run (design §3.2.1-4).
func (r *reader) paginate(ctx context.Context, resource string, page func(metav1.ListOptions) (continueToken string, err error)) error {
	cont := ""
	for {
		cont2, err := page(metav1.ListOptions{
			ResourceVersion: "0",
			Limit:           listPageLimit,
			Continue:        cont,
		})
		if err != nil {
			if apierrors.IsForbidden(err) {
				return fmt.Errorf("list %s: %w", resource, ErrForbidden)
			}
			return fmt.Errorf("list %s: %w", resource, err)
		}
		if cont2 == "" {
			return nil
		}
		cont = cont2
	}
}

// classifyGetErr maps a Get error onto the appropriate sentinel: notFound for
// IsNotFound, ErrForbidden for IsForbidden, else the raw error.
func classifyGetErr(err error, notFound error) error {
	switch {
	case apierrors.IsNotFound(err):
		return notFound
	case apierrors.IsForbidden(err):
		return ErrForbidden
	default:
		return err
	}
}
