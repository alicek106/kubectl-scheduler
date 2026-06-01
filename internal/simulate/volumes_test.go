package simulate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
)

// PoC (e) (design §1.4.2/§2.6.3): the volume filters run via the fake-clientset
// informer factory. New() -> PreFilter -> Filter must complete (all informers,
// including CSIStorageCapacity/CSIDriver and fh.ClientSet(), are fake) and the
// known PASS/FAIL/SKIPPED branches must match.

func podWithPVC(claimName string) *corev1.Pod {
	p := podRequesting("100m", "100Mi")
	p.Spec.Volumes = []corev1.Volume{{
		Name: "data",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
		},
	}}
	return p
}

func boundPVC(name, ns, pvName string) *corev1.PersistentVolumeClaim {
	sc := ""
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: pvName, StorageClassName: &sc},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
}

func zonedPV(name, zone string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{corev1.LabelTopologyZone: zone},
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes:                   []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			Capacity:                      corev1.ResourceList{corev1.ResourceStorage: resQty("1Gi")},
		},
	}
}

// TestVolumePoC_AllVolumeFiltersComplete: with a no-PVC pod, every volume filter
// completes (New + sync + PreFilter/Filter) without an engine error or panic —
// proving the fake informer wiring (incl. CSIStorageCapacity / ClientSet) syncs.
func TestVolumePoC_AllVolumeFiltersComplete(t *testing.T) {
	in := Input{Pod: podRequesting("100m", "100Mi"), Node: node("n1")}
	for _, f := range []string{names.VolumeZone, names.NodeVolumeLimits, names.VolumeBinding} {
		r := poc(t, f, in)
		if r.Verdict == VerdictSkipped {
			t.Errorf("volume filter %s did NOT complete: SKIPPED (%s)", f, r.SkipDetail)
		}
	}
}

// TestVolumePoC_VolumeZone_Mismatch: PV is in zone z1, target node is in zone
// z2 -> VolumeZone FAIL.
func TestVolumePoC_VolumeZone_Mismatch(t *testing.T) {
	n := node("n1")
	n.Labels[corev1.LabelTopologyZone] = "z2"
	pv := zonedPV("pv1", "z1")
	pvc := boundPVC("data", "default", "pv1")

	r := poc(t, names.VolumeZone, Input{
		Pod:  podWithPVC("data"),
		Node: n,
		PVCs: []*corev1.PersistentVolumeClaim{pvc},
		PVs:  []*corev1.PersistentVolume{pv},
	})
	if r.Verdict != VerdictFail {
		t.Errorf("PV zone z1 vs node zone z2: got %s (%s/%s), want FAIL", r.Verdict, r.Reason, r.SkipDetail)
	}
}

// TestVolumePoC_VolumeZone_Match: PV and node in the same zone -> PASS.
func TestVolumePoC_VolumeZone_Match(t *testing.T) {
	n := node("n1")
	n.Labels[corev1.LabelTopologyZone] = "z1"
	r := poc(t, names.VolumeZone, Input{
		Pod:  podWithPVC("data"),
		Node: n,
		PVCs: []*corev1.PersistentVolumeClaim{boundPVC("data", "default", "pv1")},
		PVs:  []*corev1.PersistentVolume{zonedPV("pv1", "z1")},
	})
	if r.Verdict != VerdictPass {
		t.Errorf("PV and node both zone z1: got %s (%s/%s), want PASS", r.Verdict, r.Reason, r.SkipDetail)
	}
}

// TestVolumePoC_VolumeBinding_DynamicUnbound_Skipped: an unbound PVC backed by a
// WaitForFirstConsumer StorageClass is dynamic provisioning decided at runtime
// -> the engine gate SKIPs VolumeBinding (no false PASS), design §2.6.2.
func TestVolumePoC_VolumeBinding_DynamicUnbound_Skipped(t *testing.T) {
	wffc := storagev1.VolumeBindingWaitForFirstConsumer
	scName := "fast"
	sc := &storagev1.StorageClass{
		ObjectMeta:        metav1.ObjectMeta{Name: scName},
		Provisioner:       "example.com/csi",
		VolumeBindingMode: &wffc,
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default"},
		Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &scName}, // unbound
	}
	r := poc(t, names.VolumeBinding, Input{
		Pod:            podWithPVC("data"),
		Node:           node("n1"),
		StorageClasses: []*storagev1.StorageClass{sc},
		PVCs:           []*corev1.PersistentVolumeClaim{pvc},
	})
	if r.Verdict != VerdictSkipped {
		t.Errorf("unbound WaitForFirstConsumer PVC: got %s (%s), want SKIPPED", r.Verdict, r.Reason)
	}
}

// TestVolumePoC_VolumeBinding_DynamicUnbound_DefaultSC: same as above but the
// PVC omits storageClassName and relies on the cluster default SC being
// WaitForFirstConsumer (review non-blocking #2). Must still SKIP.
func TestVolumePoC_VolumeBinding_DynamicUnbound_DefaultSC(t *testing.T) {
	wffc := storagev1.VolumeBindingWaitForFirstConsumer
	sc := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "default-fast",
			Annotations: map[string]string{isDefaultStorageClassAnnotation: "true"},
		},
		Provisioner:       "example.com/csi",
		VolumeBindingMode: &wffc,
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default"}, // no storageClassName
	}
	r := poc(t, names.VolumeBinding, Input{
		Pod:            podWithPVC("data"),
		Node:           node("n1"),
		StorageClasses: []*storagev1.StorageClass{sc},
		PVCs:           []*corev1.PersistentVolumeClaim{pvc},
	})
	if r.Verdict != VerdictSkipped {
		t.Errorf("unbound PVC + default WaitForFirstConsumer SC: got %s (%s), want SKIPPED", r.Verdict, r.Reason)
	}
}

// TestVolumePoC_VolumeReadError_Skips: when the cli reported a volume-read
// degrade (RBAC), every volume filter is SKIPPED rather than risk a false PASS.
func TestVolumePoC_VolumeReadError_Skips(t *testing.T) {
	in := Input{
		Pod:             podWithPVC("data"),
		Node:            node("n1"),
		VolumeReadError: "cannot read StorageClasses (RBAC forbidden)",
	}
	for _, f := range []string{names.VolumeZone, names.NodeVolumeLimits, names.VolumeBinding} {
		r := poc(t, f, in)
		if r.Verdict != VerdictSkipped {
			t.Errorf("volume read degraded: filter %s got %s, want SKIPPED", f, r.Verdict)
		}
	}
}
