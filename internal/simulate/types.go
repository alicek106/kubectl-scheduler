package simulate

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

// Input is the already-fetched cluster state needed for one simulation.
// The simulate package does not know about client-go; it consumes only
// standard API types so it can be unit-tested with fake inputs.
type Input struct {
	Pod      *corev1.Pod
	Node     *corev1.Node   // target node (also present in AllNodes)
	AllNodes []*corev1.Node // every node — full snapshot
	AllPods  []*corev1.Pod  // every pod (all namespaces) — NodeInfo placement + affinity index

	// Volume-filter inputs (design §2.6). Fed to a fake clientset informer
	// factory so VolumeBinding/VolumeZone/NodeVolumeLimits can run. May be nil
	// when the target pod references no PVCs (volume reads are then skipped).
	CSINodes        []*storagev1.CSINode
	StorageClasses  []*storagev1.StorageClass
	PVCs            []*corev1.PersistentVolumeClaim // PVCs referenced by the target pod
	PVs             []*corev1.PersistentVolume      // PVs bound to those PVCs
	VolumeReadError string                          // non-empty if volume reads degraded (RBAC) -> volume filters SKIPPED

	// Version metadata, supplied by the caller (cli layer) for the banner.
	ClusterMinor string // detected cluster minor (e.g. "1.32"); "" if unknown
}

// Verdict is the per-filter judgement.
type Verdict string

const (
	VerdictPass    Verdict = "PASS"
	VerdictFail    Verdict = "FAIL"
	VerdictSkipped Verdict = "SKIPPED" // cannot judge accurately (needs cluster-wide state, RBAC, etc.)
)

// FilterResult is the result of a single filter.
type FilterResult struct {
	Filter     string // e.g. "NodeResourcesFit"
	Verdict    Verdict
	Reason     string // human-readable reason on FAIL/SKIPPED
	SkipDetail string // on SKIPPED: why it could not be checked
}

// Outcome is the 3-state overall judgement (design B6). A single bool cannot
// distinguish "passed within the checked scope" from "definitely schedulable",
// so we use three states.
type Outcome string

const (
	// OutcomePass: every checked filter PASSed and nothing was SKIPPED.
	OutcomePass Outcome = "PASS"
	// OutcomePassWithUnchecked: every checked filter PASSed but at least one
	// filter was SKIPPED (cluster-wide / unverified). May still be blocked in reality.
	OutcomePassWithUnchecked Outcome = "PASS_WITH_UNCHECKED"
	// OutcomeFail: one or more filters FAILed.
	OutcomeFail Outcome = "FAIL"
)

// SimulationResult is the full result of a simulation.
type SimulationResult struct {
	Outcome Outcome
	Filters []FilterResult

	// Version-imitation metadata (design B4) — surfaced in the output banner.
	SimulatedMinor      string // built-in scheduler logic minor (e.g. "1.32")
	ClusterMinor        string // detected cluster minor ("" if detection failed)
	SimulatedFeatureset string // e.g. "default@v1.32"
	VersionMismatch     bool   // SimulatedMinor != ClusterMinor
	LowConfidence       bool   // minor spread >= 1, etc.
}

// computeOutcome derives the 3-state Outcome from per-filter verdicts:
//   - any FAIL          -> FAIL
//   - else any SKIPPED  -> PASS_WITH_UNCHECKED
//   - else (all PASS)   -> PASS
func computeOutcome(filters []FilterResult) Outcome {
	hasSkipped := false
	for _, f := range filters {
		switch f.Verdict {
		case VerdictFail:
			return OutcomeFail
		case VerdictSkipped:
			hasSkipped = true
		}
	}
	if hasSkipped {
		return OutcomePassWithUnchecked
	}
	return OutcomePass
}

// Simulator is the engine abstraction. The hybrid (framework) implementation is
// the default; swapping in a fallback (re-implementation) does not affect the
// cli layer.
type Simulator interface {
	Simulate(ctx context.Context, in Input) (*SimulationResult, error)
}
