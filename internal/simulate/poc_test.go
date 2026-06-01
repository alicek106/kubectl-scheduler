package simulate

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
)

// A1 PoC (design §1.4.2): drive the in-tree Filter plugins through the P1 path
// (frameworkruntime.NewFramework -> RunPreFilterPlugins -> RunFilterPlugins) on
// the full-cluster read-only snapshot, and verify:
//   (a) build      — this file building + compiling proves the import works
//   (b) gate default — NewInTreeRegistry uses the pinned version's
//                      DefaultFeatureGate; verified implicitly by construction
//   (c) completion — PreFilter+Filter (incl. the SharedLister method surface of
//                    InterPodAffinity/PodTopologySpread) complete without
//                    panicking (no engine-internal Error verdict)
//   (d) match      — known PASS/FAIL cases match the real scheduler's judgement
//   (e) volume     — VolumeBinding/NodeVolumeLimits/VolumeZone complete via the
//                    fake-clientset informer factory (volumes_test.go)

func resQty(s string) resource.Quantity { return resource.MustParse(s) }

func node(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"kubernetes.io/hostname": name}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resQty("2"),
				corev1.ResourceMemory: resQty("4Gi"),
				corev1.ResourcePods:   resQty("110"),
			},
		},
	}
}

func podRequesting(cpu, mem string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "c",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resQty(cpu),
						corev1.ResourceMemory: resQty(mem),
					},
				},
			}},
		},
	}
}

// poc runs one filter via the engine on a full-cluster snapshot and returns its
// FilterResult. It uses the same ensureNodeInSnapshot invariant the engine uses
// so the target node always has a NodeInfo.
func poc(t *testing.T, filter string, in Input) FilterResult {
	t.Helper()
	sim := NewFrameworkSimulator().(*frameworkSimulator)
	snapshot := newClusterSnapshot(ensureNodeInSnapshot(in.AllNodes, in.Node), in.AllPods)
	ni, err := snapshot.Get(in.Node.Name)
	if err != nil {
		t.Fatalf("get nodeinfo: %v", err)
	}
	sc := newSimContext(context.Background(), in)
	return sim.runFilter(context.Background(), enabledFilter{name: filter, kind: kindOf(filter)}, snapshot, ni, in.Pod, sc)
}

// kindOf returns the filterKind the engine uses for the named filter, so the PoC
// drives it exactly as production does (volume gate included).
func kindOf(filter string) filterKind {
	for _, ef := range enabledFilters {
		if ef.name == filter {
			return ef.kind
		}
	}
	return kindClusterWide
}

func TestA1PoC_AllNodeLocalFiltersComplete(t *testing.T) {
	// Criterion (c): every node-local filter completes without an engine-internal
	// Error on the full-cluster snapshot.
	in := Input{Pod: podRequesting("100m", "100Mi"), Node: node("n1")}
	for _, f := range []string{
		names.NodeUnschedulable,
		names.TaintToleration,
		names.NodeAffinity,
		names.NodeName,
		names.NodePorts,
		names.NodeResourcesFit,
	} {
		r := poc(t, f, in)
		if r.Verdict == VerdictSkipped {
			t.Errorf("filter %s did NOT complete on snapshot: SKIPPED (%s) — demote in §2.3", f, r.SkipDetail)
		}
	}
}

func TestA1PoC_NodeResourcesFit(t *testing.T) {
	// Criterion (d): NodeResourcesFit PASS when it fits, FAIL when it does not.
	tests := []struct {
		name string
		in   Input
		want Verdict
	}{
		{"fits", Input{Pod: podRequesting("1", "1Gi"), Node: node("n1")}, VerdictPass},
		{"too much cpu", Input{Pod: podRequesting("4", "1Gi"), Node: node("n1")}, VerdictFail},
		{"too much mem", Input{Pod: podRequesting("1", "8Gi"), Node: node("n1")}, VerdictFail},
		{
			"fits but node already loaded",
			Input{
				Pod:     podRequesting("1500m", "1Gi"),
				Node:    node("n1"),
				AllPods: []*corev1.Pod{withNodeName(podRequesting("1", "1Gi"), "n1")},
			},
			VerdictFail, // 1 (existing) + 1.5 (new) > 2 allocatable
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := poc(t, names.NodeResourcesFit, tt.in)
			if r.Verdict != tt.want {
				t.Errorf("got %s (%s / %s), want %s", r.Verdict, r.Reason, r.SkipDetail, tt.want)
			}
		})
	}
}

func TestA1PoC_NodeUnschedulable(t *testing.T) {
	cordoned := node("n1")
	cordoned.Spec.Unschedulable = true
	r := poc(t, names.NodeUnschedulable, Input{Pod: podRequesting("100m", "100Mi"), Node: cordoned})
	if r.Verdict != VerdictFail {
		t.Errorf("cordoned node: got %s, want FAIL", r.Verdict)
	}
}

func TestA1PoC_TaintToleration(t *testing.T) {
	tainted := node("n1")
	tainted.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}
	r := poc(t, names.TaintToleration, Input{Pod: podRequesting("100m", "100Mi"), Node: tainted})
	if r.Verdict != VerdictFail {
		t.Errorf("tainted node, no toleration: got %s, want FAIL", r.Verdict)
	}
}

func TestA1PoC_NodeAffinity(t *testing.T) {
	pod := podRequesting("100m", "100Mi")
	pod.Spec.NodeSelector = map[string]string{"disktype": "ssd"} // node has no such label
	r := poc(t, names.NodeAffinity, Input{Pod: pod, Node: node("n1")})
	if r.Verdict != VerdictFail {
		t.Errorf("node selector mismatch: got %s, want FAIL", r.Verdict)
	}
}

func TestA1PoC_NodePorts(t *testing.T) {
	existing := withNodeName(podRequesting("100m", "100Mi"), "n1")
	existing.Spec.Containers[0].Ports = []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}}
	pod := podRequesting("100m", "100Mi")
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}}
	r := poc(t, names.NodePorts, Input{Pod: pod, Node: node("n1"), AllPods: []*corev1.Pod{existing}})
	if r.Verdict != VerdictFail {
		t.Errorf("host port conflict: got %s, want FAIL", r.Verdict)
	}
}

func withNodeName(p *corev1.Pod, n string) *corev1.Pod {
	p = p.DeepCopy()
	p.Spec.NodeName = n
	return p
}

// zoneNode returns a node labeled with a shared zone topology key so inter-pod
// (anti)affinity terms keyed on the zone can match pods across DIFFERENT nodes.
func zoneNode(name, zone string) *corev1.Node {
	n := node(name)
	n.Labels["topology.kubernetes.io/zone"] = zone
	return n
}

// labeledPod returns a scheduled pod carrying the given label so other pods'
// (anti)affinity selectors can match it.
func labeledPod(name, nodeName string, labels map[string]string) *corev1.Pod {
	p := podRequesting("100m", "100Mi")
	p.Name = name
	p.Labels = labels
	p.Spec.NodeName = nodeName
	return p
}

const zoneKey = "topology.kubernetes.io/zone"

// TestA1PoC_InterPodAffinity_Differential is the B7 (d) differential gate: a
// matching pod on a DIFFERENT node must be visible in the full-cluster snapshot.
// If the snapshot only saw the target node, required anti-affinity would
// false-PASS and required affinity would false-FAIL. With the full snapshot:
//   - required anti-affinity + matching pod in the same zone on another node => FAIL
//   - required affinity      + matching pod in the same zone on another node => PASS
func TestA1PoC_InterPodAffinity_Differential(t *testing.T) {
	// Two nodes in the same zone; the matching pod lives on n2 (NOT the target n1).
	nodes := []*corev1.Node{zoneNode("n1", "z1"), zoneNode("n2", "z1")}
	matching := labeledPod("other", "n2", map[string]string{"app": "web"})

	antiPod := podRequesting("100m", "100Mi")
	antiPod.Spec.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			TopologyKey:   zoneKey,
		}},
	}}
	rAnti := poc(t, names.InterPodAffinity, Input{
		Pod: antiPod, Node: zoneNode("n1", "z1"), AllNodes: nodes, AllPods: []*corev1.Pod{matching},
	})
	if rAnti.Verdict != VerdictFail {
		t.Errorf("required anti-affinity with matching pod on another node in same zone: got %s (%s), want FAIL", rAnti.Verdict, rAnti.Reason)
	}

	affPod := podRequesting("100m", "100Mi")
	affPod.Spec.Affinity = &corev1.Affinity{PodAffinity: &corev1.PodAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
			LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			TopologyKey:   zoneKey,
		}},
	}}
	rAff := poc(t, names.InterPodAffinity, Input{
		Pod: affPod, Node: zoneNode("n1", "z1"), AllNodes: nodes, AllPods: []*corev1.Pod{matching},
	})
	if rAff.Verdict != VerdictPass {
		t.Errorf("required affinity with matching pod on another node in same zone: got %s (%s/%s), want PASS", rAff.Verdict, rAff.Reason, rAff.SkipDetail)
	}
}

// TestA1PoC_PodTopologySpread_Completes verifies PodTopologySpread completes via
// the full snapshot (PreFilter scans every node's domain counts). A pod with a
// maxSkew=1 hostname constraint and an existing matching pod on n2 should still
// PASS onto the empty n1 (placing it balances the spread).
func TestA1PoC_PodTopologySpread_Completes(t *testing.T) {
	nodes := []*corev1.Node{node("n1"), node("n2")}
	existing := labeledPod("other", "n2", map[string]string{"app": "web"})
	pod := podRequesting("100m", "100Mi")
	pod.Labels = map[string]string{"app": "web"}
	pod.Spec.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{
		MaxSkew:           1,
		TopologyKey:       "kubernetes.io/hostname",
		WhenUnsatisfiable: corev1.DoNotSchedule,
		LabelSelector:     &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
	}}
	r := poc(t, names.PodTopologySpread, Input{
		Pod: pod, Node: node("n1"), AllNodes: nodes, AllPods: []*corev1.Pod{existing},
	})
	if r.Verdict != VerdictPass {
		t.Errorf("topology spread onto empty node balancing skew: got %s (%s/%s), want PASS", r.Verdict, r.Reason, r.SkipDetail)
	}
}
