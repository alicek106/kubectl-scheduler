package simulate

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
)

// simContext holds the per-simulation wiring shared across every filter: one
// fake-clientset informer factory (carrying all read-only state), the volume
// static/dynamic gate, and any volume-read degrade reason. It is computed once
// per Simulate.
//
// A single SharedInformerFactory is required by more than the volume filters:
// InterPodAffinity reads the Namespaces lister and PodTopologySpread reads the
// Services/ReplicaSets/etc. listers from h.SharedInformerFactory() in their
// New() — passing nil panics (interpodaffinity) or errors (podtopologyspread).
// So we always build one factory over a fake clientset and give it to every
// framework. The factory's list/watch hit only the in-memory fake, never the
// real apiserver (read-only invariant, design §2.6.1 (1)).
type simContext struct {
	informers *fakeInformers
	gate      volumeGate
	// volumeReadError is non-empty when the read-only volume reads degraded (e.g.
	// RBAC Forbidden). The volume filters are then SKIPPED to avoid a false PASS.
	volumeReadError string
}

// newSimContext builds the shared context for a simulation. The informer factory
// is always built (even with empty objects) so the plugins can construct their
// listers; the informers list/watch only the in-memory fake.
func newSimContext(_ context.Context, in Input) *simContext {
	return &simContext{
		informers:       buildFakeInformers(in),
		gate:            computeVolumeGate(in),
		volumeReadError: in.VolumeReadError,
	}
}

// volumeSkipDetail returns a non-empty SKIPPED reason for the given volume
// filter, or "" if it should run. Order: volume-read degrade (all volume
// filters) -> VolumeBinding dynamic/WaitForFirstConsumer gate (VolumeBinding
// only). Cluster-wide (non-volume) filters never skip here.
func (sc *simContext) volumeSkipDetail(filter string) string {
	if sc.volumeReadError != "" {
		return fmt.Sprintf("volume metadata unavailable: %s", sc.volumeReadError)
	}
	if filter == names.VolumeBinding && sc.gate.skip {
		return sc.gate.detail
	}
	return ""
}

// Well-known annotations for default-StorageClass resolution (design §2.6.2,
// review non-blocking #2). The constants are not exported by k8s.io/api in a
// convenient place, so we declare them here.
const (
	isDefaultStorageClassAnnotation = "storageclass.kubernetes.io/is-default-class"
	// betaDefaultStorageClassAnnotation is the legacy beta key still honored by
	// the apiserver default-SC admission controller.
	betaDefaultStorageClassAnnotation = "storageclass.beta.kubernetes.io/is-default-class"
	// betaStorageClassAnnotation is the legacy per-PVC storage-class selector.
	betaStorageClassAnnotation = "volume.beta.kubernetes.io/storage-class"
)

// volumeCacheSyncTimeout bounds informer cache sync. fake-clientset listers
// populate immediately, so this is a generous safety net (design §2.6.1 (3)).
const volumeCacheSyncTimeout = 10 * time.Second

// fakeInformers carries a fake clientset and its informer factory. The
// factory's list/watch go only to the in-memory fake ObjectTracker — never to
// the real apiserver — so the read-only invariant holds (design §2.6.1 (1)).
type fakeInformers struct {
	client  *fake.Clientset
	factory informers.SharedInformerFactory
}

// buildFakeInformers constructs a fake clientset from all read-only objects the
// plugins may need via h.SharedInformerFactory(): nodes/pods (volume_binding,
// and topology counting), storage objects (volume filters), and — implicitly,
// served empty — namespaces/services/replicasets/etc. that InterPodAffinity and
// PodTopologySpread reference. The factory is returned un-started; the caller
// starts and syncs it AFTER NewFramework, because each plugin's New() registers
// the informers it needs (so they must exist before Start). We never call any
// write method on the fake.
//
// CSIDriver and CSIStorageCapacity informers are registered by
// volume_binding.New(); we do not fetch those from the cluster (empty is fine —
// review non-blocking #1/#3): the fake serves an empty list and the cache still
// syncs, and fh.ClientSet() is this same fake so any binder write path can never
// reach the real apiserver.
func buildFakeInformers(in Input) *fakeInformers {
	objs := make([]runtime.Object, 0, len(in.CSINodes)+len(in.StorageClasses)+len(in.PVCs)+len(in.PVs)+len(in.AllNodes)+len(in.AllPods))
	for _, o := range in.CSINodes {
		objs = append(objs, o)
	}
	for _, o := range in.StorageClasses {
		objs = append(objs, o)
	}
	for _, o := range in.PVCs {
		objs = append(objs, o)
	}
	for _, o := range in.PVs {
		objs = append(objs, o)
	}
	for _, o := range in.AllNodes {
		objs = append(objs, o)
	}
	for _, o := range in.AllPods {
		objs = append(objs, o)
	}
	client := fake.NewSimpleClientset(objs...)
	return &fakeInformers{
		client:  client,
		factory: informers.NewSharedInformerFactory(client, 0),
	}
}

// startAndSync starts the factory and waits for the informers that the plugins
// registered (via fh.SharedInformerFactory()...() during New()) to sync. It
// returns an error if any informer fails to sync before the timeout, in which
// case the engine demotes the affected filter to SKIPPED rather than risk a
// false PASS (design §2.6.1 (3)).
func (v *fakeInformers) startAndSync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, volumeCacheSyncTimeout)
	defer cancel()
	stop := ctx.Done()
	v.factory.Start(stop)
	synced := v.factory.WaitForCacheSync(stop)
	for typ, ok := range synced {
		if !ok {
			return fmt.Errorf("informer cache for %v did not sync", typ)
		}
	}
	return nil
}

// volumeGate is the engine's static/dynamic decision for the volume filters
// (design §2.6.2). It is computed once per simulation from the target pod's
// PVCs + their effective StorageClasses.
type volumeGate struct {
	// skip is true when at least one referenced PVC is unbound and bound by a
	// WaitForFirstConsumer StorageClass — dynamic provisioning decided at
	// runtime, which we cannot judge. The whole VolumeBinding verdict becomes
	// SKIPPED to avoid a false PASS.
	skip   bool
	detail string
}

// computeVolumeGate inspects the target pod's PVC references. For each PVC:
//   - already bound (Spec.VolumeName != "") -> static, judged by the real Filter.
//   - effective SC volumeBindingMode == Immediate -> static, judged by the Filter.
//   - effective SC volumeBindingMode == WaitForFirstConsumer and unbound ->
//     dynamic, SKIPPED (cannot decide runtime provisioning).
//
// The effective SC is the PVC's Spec.StorageClassName, else the legacy beta
// annotation, else the cluster default SC (review non-blocking #2).
func computeVolumeGate(in Input) volumeGate {
	if len(in.PVCs) == 0 {
		return volumeGate{}
	}
	scByName := make(map[string]*storagev1.StorageClass, len(in.StorageClasses))
	var defaultSC *storagev1.StorageClass
	for _, sc := range in.StorageClasses {
		scByName[sc.Name] = sc
		if isDefaultSC(sc) {
			defaultSC = sc
		}
	}

	for _, pvc := range in.PVCs {
		if pvc.Spec.VolumeName != "" {
			continue // already bound -> static
		}
		sc := effectiveStorageClass(pvc, scByName, defaultSC)
		if sc == nil {
			// No class resolvable: static provisioning path (the Filter matches
			// against existing PVs); leave to the real Filter.
			continue
		}
		if sc.VolumeBindingMode != nil && *sc.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
			return volumeGate{
				skip:   true,
				detail: fmt.Sprintf("dynamic provisioning (WaitForFirstConsumer) for unbound PVC %s/%s decided at runtime", pvc.Namespace, pvc.Name),
			}
		}
	}
	return volumeGate{}
}

// effectiveStorageClass resolves the StorageClass that applies to a PVC, in the
// order: Spec.StorageClassName -> legacy beta annotation -> cluster default SC.
func effectiveStorageClass(pvc *corev1.PersistentVolumeClaim, scByName map[string]*storagev1.StorageClass, defaultSC *storagev1.StorageClass) *storagev1.StorageClass {
	if pvc.Spec.StorageClassName != nil {
		if *pvc.Spec.StorageClassName == "" {
			// Explicit empty string opts out of dynamic provisioning entirely.
			return nil
		}
		return scByName[*pvc.Spec.StorageClassName]
	}
	if name, ok := pvc.Annotations[betaStorageClassAnnotation]; ok && name != "" {
		return scByName[name]
	}
	return defaultSC
}

// isDefaultSC reports whether a StorageClass is marked as the cluster default
// via either the GA or legacy beta annotation.
func isDefaultSC(sc *storagev1.StorageClass) bool {
	return sc.Annotations[isDefaultStorageClassAnnotation] == "true" ||
		sc.Annotations[betaDefaultStorageClassAnnotation] == "true"
}
