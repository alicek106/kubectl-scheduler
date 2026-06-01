package cli

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/alicek106/kubectl-scheduler/internal/kube"
	"github.com/alicek106/kubectl-scheduler/internal/simulate"
)

// run orchestrates: resolve flags -> fetch (read-only) -> simulate -> render.
// Kept thin; all cluster access goes through kube, all judging through simulate.
func run(ctx context.Context, flags *genericclioptions.ConfigFlags, podName, nodeName string, noColor bool) error {
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	namespace, err := kube.Namespace(flags)
	if err != nil {
		return err
	}

	clients, err := kube.NewClients(flags)
	if err != nil {
		return err
	}
	reader := kube.NewReader(clients.Clientset)

	in, err := fetchInput(ctx, reader, kube.NewVersionProvider(clients.Discovery), namespace, podName, nodeName)
	if err != nil {
		return err
	}

	sim := simulate.NewFrameworkSimulator()
	result, err := sim.Simulate(ctx, *in)
	if err != nil {
		return fmt.Errorf("simulate: %w", err)
	}

	out := stdout()
	return render(out, result, RenderOptions{
		PodName:   podName,
		NodeName:  nodeName,
		Namespace: namespace,
		Color:     colorEnabled(out, noColor),
	})
}

// fetchInput performs the read-only reads (design §3) and assembles the
// simulate.Input. The full Node/Pod lists drive the cluster-wide snapshot; the
// volume reads (only when the target pod references PVCs) feed the volume
// filters. RBAC Forbidden on a list degrades gracefully: the dependent filters
// are SKIPPED rather than failing the whole run.
func fetchInput(
	ctx context.Context,
	reader kube.ClusterReader,
	versions kube.ServerVersionProvider,
	namespace, podName, nodeName string,
) (*simulate.Input, error) {
	pod, err := reader.GetPod(ctx, namespace, podName)
	if err != nil {
		return nil, err
	}
	node, err := reader.GetNode(ctx, nodeName)
	if err != nil {
		return nil, err
	}

	allNodes, err := reader.ListAllNodes(ctx)
	if err != nil {
		// Node list is required for the cluster snapshot. If it is forbidden we
		// cannot build any node-level judgement, so surface a clear error.
		return nil, fmt.Errorf("list nodes (needed for the cluster snapshot): %w", err)
	}
	// The simulate engine defensively ensures the target node is in the snapshot
	// (it owns that invariant), so we pass the list through as-is.

	allPods, err := reader.ListAllPods(ctx)
	if err != nil {
		if errors.Is(err, kube.ErrForbidden) {
			return nil, fmt.Errorf("list pods across all namespaces is required for the cluster snapshot but was forbidden (RBAC) — grant pods:list cluster-wide: %w", err)
		}
		return nil, err
	}

	in := &simulate.Input{
		Pod:      pod,
		Node:     node,
		AllNodes: allNodes,
		AllPods:  allPods,
	}

	fetchVolumeState(ctx, reader, pod, namespace, in)

	if major, minor, verr := versions.ServerMinor(ctx); verr != nil {
		fmt.Fprintf(stderr(), "warning: could not detect cluster version: %v\n", verr)
	} else if major != "" && minor != "" {
		in.ClusterMinor = major + "." + minor
	}

	return in, nil
}

// fetchVolumeState fetches the read-only volume objects the volume filters need
// (design §2.6, §3.1 #6). It is a no-op when the target pod references no PVCs
// (cost minimization). RBAC Forbidden on any volume read sets VolumeReadError so
// the engine SKIPs the volume filters rather than risk a false PASS.
func fetchVolumeState(ctx context.Context, reader kube.ClusterReader, pod *corev1.Pod, namespace string, in *simulate.Input) {
	pvcNames := referencedPVCNames(pod)
	if len(pvcNames) == 0 {
		return
	}

	csiNodes, err := reader.ListCSINodes(ctx)
	if err != nil {
		in.VolumeReadError = volumeDegradeReason("CSINodes", err)
		return
	}
	scs, err := reader.ListStorageClasses(ctx)
	if err != nil {
		in.VolumeReadError = volumeDegradeReason("StorageClasses", err)
		return
	}
	in.CSINodes = csiNodes
	in.StorageClasses = scs

	for _, name := range pvcNames {
		pvc, err := reader.GetPVC(ctx, namespace, name)
		if err != nil {
			if errors.Is(err, kube.ErrPodNotFound) {
				// PVC referenced by the pod does not exist. This is itself a
				// scheduling problem the VolumeBinding filter reports; pass the
				// (absent) state through — the filter will report the missing PVC.
				continue
			}
			in.VolumeReadError = volumeDegradeReason("PVC "+name, err)
			return
		}
		in.PVCs = append(in.PVCs, pvc)
		if pvc.Spec.VolumeName != "" {
			pv, err := reader.GetPV(ctx, pvc.Spec.VolumeName)
			if err != nil {
				if errors.Is(err, kube.ErrPodNotFound) {
					continue
				}
				in.VolumeReadError = volumeDegradeReason("PV "+pvc.Spec.VolumeName, err)
				return
			}
			in.PVs = append(in.PVs, pv)
		}
	}
}

// referencedPVCNames returns the names of PVCs referenced by the pod's volumes.
func referencedPVCNames(pod *corev1.Pod) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		n := v.PersistentVolumeClaim.ClaimName
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		names = append(names, n)
	}
	return names
}

// volumeDegradeReason builds the VolumeReadError detail, distinguishing RBAC
// Forbidden from other read failures.
func volumeDegradeReason(what string, err error) string {
	if errors.Is(err, kube.ErrForbidden) {
		return fmt.Sprintf("cannot read %s (RBAC forbidden)", what)
	}
	return fmt.Sprintf("cannot read %s: %v", what, err)
}
