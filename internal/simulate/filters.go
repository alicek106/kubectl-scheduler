package simulate

import "k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"

// SimulatedMinor is the kube-scheduler logic minor compiled into this binary.
// It tracks the k8s.io/kubernetes pin in go.mod (v1.32.x). If you bump the pin,
// bump this too (and re-run the PoC).
const SimulatedMinor = "1.32"

// SimulatedFeatureset names the feature-gate set the engine runs with. The
// engine reuses the pinned version's default feature gate (component-base
// global DefaultFeatureGate via NewInTreeRegistry), so the featureset is the
// pinned minor's GA/default-on gates. The target cluster's real
// --feature-gates cannot be read over normal RBAC, so gate-driven divergence
// is acknowledged in the banner, not silently assumed away (design §1.5.1).
const SimulatedFeatureset = "default@v1.32"

// filterKind groups filters by what they need to run, which controls how the
// engine wires them (plain vs. volume informer factory).
type filterKind int

const (
	// kindClusterWide: judged accurately from the full cluster snapshot
	// (SharedLister) alone — no extra resources. Design §2.3 ① (promoted).
	kindClusterWide filterKind = iota
	// kindVolume: needs the fake-clientset informer factory carrying PV/PVC/SC/
	// CSINode (design §2.3 ②/③, §2.6). VolumeBinding additionally needs the
	// static/dynamic gate (engine pre-checks; dynamic-unbound -> SKIPPED).
	kindVolume
)

// enabledFilter is a filter the engine runs through the P1
// (frameworkruntime.NewFramework) PreFilter -> Filter path.
type enabledFilter struct {
	name string
	kind filterKind
}

// enabledFilters are every filter the engine runs this round.
//
//   - ① cluster-wide accurate (full snapshot): the original five node-local
//     filters + NodeName + InterPodAffinity + PodTopologySpread (promoted now
//     that the snapshot holds every node and pod).
//   - ②/③ volume (Round 5, fake-clientset informer factory): VolumeZone,
//     NodeVolumeLimits, VolumeBinding (static accurate; dynamic-unbound SKIPPED).
var enabledFilters = []enabledFilter{
	{name: names.NodeUnschedulable, kind: kindClusterWide},
	{name: names.TaintToleration, kind: kindClusterWide},
	{name: names.NodeAffinity, kind: kindClusterWide},
	{name: names.NodeName, kind: kindClusterWide},
	{name: names.NodePorts, kind: kindClusterWide},
	{name: names.NodeResourcesFit, kind: kindClusterWide},
	{name: names.InterPodAffinity, kind: kindClusterWide},
	{name: names.PodTopologySpread, kind: kindClusterWide},
	{name: names.VolumeZone, kind: kindVolume},
	{name: names.NodeVolumeLimits, kind: kindVolume},
	{name: names.VolumeBinding, kind: kindVolume},
}

// preFilterCapable lists which enabled filters implement the PreFilter
// extension point in the pinned version. Enabling a Filter-only plugin on the
// PreFilter extension point makes NewFramework fail ("does not extend
// PreFilterPlugin"), so we only wire PreFilter for these. Confirmed for v1.32:
// TaintToleration / NodeUnschedulable / NodeName are Filter-only; the rest
// implement PreFilter.
var preFilterCapable = map[string]bool{
	names.NodeResourcesFit:  true,
	names.NodeAffinity:      true,
	names.NodePorts:         true,
	names.InterPodAffinity:  true,
	names.PodTopologySpread: true,
	names.VolumeZone:        true,
	names.NodeVolumeLimits:  true,
	names.VolumeBinding:     true,
}
