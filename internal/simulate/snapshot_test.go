package simulate

import (
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// B7 semantic-equivalence gate (design §4.3, blocking).
//
// clusterSnapshot directly mirrors the real scheduler's internal/cache.Snapshot
// (which we cannot import). A silent drift between our HavePodsWith...List() /
// IsPVCUsedByPods() and framework's own AddPod indexing would cause a quiet
// false-PASS/FAIL. This test builds the EXPECTED values INDEPENDENTLY of the
// implementation under test — it constructs framework.NewNodeInfo()+AddPod()
// from scratch here and traverses them with the plain conditions — then asserts
// the clusterSnapshot returns exactly the same node sets / PVC membership.
//
// The expectation derivation MUST NOT call any clusterSnapshot code.

// --- test fixtures (independent of production builders) ---

func tnode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// plainPod: scheduled pod with no affinity and no PVC.
func plainPod(name, ns, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       corev1.PodSpec{NodeName: nodeName},
	}
}

func withPodAffinity(p *corev1.Pod) *corev1.Pod {
	p = p.DeepCopy()
	p.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}},
			TopologyKey:   "kubernetes.io/hostname",
		}},
	}}
	return p
}

func withPodAntiAffinity(p *corev1.Pod) *corev1.Pod {
	p = p.DeepCopy()
	p.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "y"}},
			TopologyKey:   "kubernetes.io/hostname",
		}},
	}}
	return p
}

func withPVC(p *corev1.Pod, claimName string) *corev1.Pod {
	p = p.DeepCopy()
	p.Spec.Volumes = append(p.Spec.Volumes, corev1.Volume{
		Name: "v-" + claimName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claimName},
		},
	})
	return p
}

// expectedDerivation builds the expected node sets / PVC-used keys from scratch
// using framework.NewNodeInfo()+AddPod() — the single source of truth — without
// touching any clusterSnapshot code.
func expectedDerivation(nodes []*corev1.Node, pods []*corev1.Pod) (withAff, withAnti map[string]bool, pvcUsed map[string]bool) {
	infos := map[string]*framework.NodeInfo{}
	for _, n := range nodes {
		ni := framework.NewNodeInfo()
		ni.SetNode(n)
		infos[n.Name] = ni
	}
	for _, p := range pods {
		if p.Spec.NodeName == "" {
			continue
		}
		if ni, ok := infos[p.Spec.NodeName]; ok {
			ni.AddPod(p)
		}
	}
	withAff = map[string]bool{}
	withAnti = map[string]bool{}
	pvcUsed = map[string]bool{}
	for name, ni := range infos {
		if len(ni.PodsWithAffinity) > 0 {
			withAff[name] = true
		}
		if len(ni.PodsWithRequiredAntiAffinity) > 0 {
			withAnti[name] = true
		}
		for key := range ni.PVCRefCounts {
			pvcUsed[key] = true
		}
	}
	return
}

func nodeNameSet(infos []*framework.NodeInfo) map[string]bool {
	out := map[string]bool{}
	for _, ni := range infos {
		if ni.Node() != nil {
			out[ni.Node().Name] = true
		}
	}
	return out
}

func TestClusterSnapshot_SemanticEquivalence(t *testing.T) {
	tests := []struct {
		name  string
		nodes []*corev1.Node
		pods  []*corev1.Pod
	}{
		{
			name:  "no pods",
			nodes: []*corev1.Node{tnode("n1"), tnode("n2")},
		},
		{
			name:  "affinity only",
			nodes: []*corev1.Node{tnode("n1"), tnode("n2")},
			pods: []*corev1.Pod{
				withPodAffinity(plainPod("a", "default", "n1")),
				plainPod("b", "default", "n2"),
			},
		},
		{
			name:  "anti-affinity only",
			nodes: []*corev1.Node{tnode("n1"), tnode("n2")},
			pods: []*corev1.Pod{
				withPodAntiAffinity(plainPod("a", "default", "n2")),
			},
		},
		{
			name:  "mixed affinity + anti-affinity on different nodes",
			nodes: []*corev1.Node{tnode("n1"), tnode("n2"), tnode("n3")},
			pods: []*corev1.Pod{
				withPodAffinity(plainPod("a", "default", "n1")),
				withPodAntiAffinity(plainPod("b", "default", "n2")),
				plainPod("c", "kube-system", "n3"),
			},
		},
		{
			name:  "pvc references",
			nodes: []*corev1.Node{tnode("n1"), tnode("n2")},
			pods: []*corev1.Pod{
				withPVC(plainPod("a", "default", "n1"), "data"),
				withPVC(plainPod("b", "team", "n2"), "logs"),
			},
		},
		{
			name:  "unscheduled and unknown-node pods ignored",
			nodes: []*corev1.Node{tnode("n1")},
			pods: []*corev1.Pod{
				withPodAffinity(plainPod("pending", "default", "")),   // no nodeName
				withPodAffinity(plainPod("ghost", "default", "gone")), // node not in list
				withPVC(plainPod("real", "default", "n1"), "data"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantAff, wantAnti, wantPVC := expectedDerivation(tt.nodes, tt.pods)

			snap := newClusterSnapshot(tt.nodes, tt.pods)

			gotAffList, err := snap.HavePodsWithAffinityList()
			if err != nil {
				t.Fatal(err)
			}
			gotAntiList, err := snap.HavePodsWithRequiredAntiAffinityList()
			if err != nil {
				t.Fatal(err)
			}
			gotAff := nodeNameSet(gotAffList)
			gotAnti := nodeNameSet(gotAntiList)

			if !equalSet(gotAff, wantAff) {
				t.Errorf("HavePodsWithAffinityList nodes = %v, want %v", keys(gotAff), keys(wantAff))
			}
			if !equalSet(gotAnti, wantAnti) {
				t.Errorf("HavePodsWithRequiredAntiAffinityList nodes = %v, want %v", keys(gotAnti), keys(wantAnti))
			}
			// affinity and anti-affinity must never be merged into one list.
			for n := range gotAff {
				if wantAnti[n] && !wantAff[n] {
					t.Errorf("node %s wrongly in affinity list", n)
				}
			}

			for key := range wantPVC {
				if !snap.IsPVCUsedByPods(key) {
					t.Errorf("IsPVCUsedByPods(%q) = false, want true", key)
				}
			}
			if snap.IsPVCUsedByPods("default/never-referenced") {
				t.Errorf("IsPVCUsedByPods(unreferenced) = true, want false")
			}
		})
	}
}

func equalSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
