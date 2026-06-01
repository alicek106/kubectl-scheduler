package simulate

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/apis/config"
	configlatest "k8s.io/kubernetes/pkg/scheduler/apis/config/latest"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/names"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	schedmetrics "k8s.io/kubernetes/pkg/scheduler/metrics"
)

// frameworkSimulator is the hybrid engine: it reuses the real in-tree
// kube-scheduler Filter plugins (pinned to a specific minor via go.mod) and
// drives them through the P1 path (frameworkruntime.NewFramework ->
// RunPreFilterPlugins -> RunFilterPlugins) on a read-only full-cluster snapshot
// (design §1.4.1, §2.4).
//
// Each enabled filter runs in its own minimal Framework so that:
//   - PreFilter -> Filter ordering is enforced per plugin (cycle state filled
//     before Filter when the plugin has PreFilter; never skipped) — design §1.4.3.
//   - the failing/skip plugin is unambiguously attributable to one filter.
//
// Cluster-wide filters share the read-only clusterSnapshot SharedLister. Volume
// filters additionally need a SharedInformerFactory carrying PV/PVC/SC/CSINode
// (+ the informers volume_binding.New registers): we build that from a fake
// clientset over the read-only objects so the real apiserver is never watched or
// written (design §2.6).
type frameworkSimulator struct {
	registry frameworkruntime.Registry
	// pluginConfig carries the pinned version's defaulted plugin args (e.g.
	// NodeResourcesFitArgs, NodeAffinityArgs, VolumeBindingArgs). NewFramework
	// requires these for plugins that take args; we source them from the
	// scheduler's own default config so they match the pinned version exactly.
	pluginConfig []config.PluginConfig
}

var _ Simulator = (*frameworkSimulator)(nil)

// NewFrameworkSimulator builds the default hybrid simulator. The in-tree
// registry is constructed with the pinned version's default feature gate
// (component-base DefaultFeatureGate), which is our fixed featureset. Plugin
// args are taken from the scheduler's defaulted config (same pinned version).
func NewFrameworkSimulator() Simulator {
	// The framework's instrumented plugins reference scheduler metric vecs that
	// are nil until registered; Register() is a sync.Once, so this is safe to
	// call on every construction. Without it, NewFramework panics.
	schedmetrics.Register()
	s := &frameworkSimulator{registry: plugins.NewInTreeRegistry()}
	if def, err := configlatest.Default(); err == nil && len(def.Profiles) > 0 {
		s.pluginConfig = def.Profiles[0].PluginConfig
	}
	return s
}

// newFrameworkForFilter constructs a minimal Framework with one filter plugin
// enabled at Filter (and PreFilter if it is PreFilter-capable), plus the
// required QueueSort + Bind plugins (NewFramework demands exactly one QueueSort
// and at least one Bind) and the pinned version's defaulted plugin args.
//
// extraOpts lets volume filters add WithInformerFactory / WithClientSet.
func (s *frameworkSimulator) newFrameworkForFilter(ctx context.Context, name string, lister framework.SharedLister, extraOpts ...frameworkruntime.Option) (framework.Framework, error) {
	prefilter := config.PluginSet{}
	if preFilterCapable[name] {
		prefilter.Enabled = []config.Plugin{{Name: name}}
	}
	profile := &config.KubeSchedulerProfile{
		SchedulerName: "kubectl-schedule",
		Plugins: &config.Plugins{
			// NewFramework requires exactly one QueueSort plugin and at least one
			// Bind plugin per profile, even though we only drive PreFilter/Filter.
			QueueSort: config.PluginSet{Enabled: []config.Plugin{{Name: names.PrioritySort}}},
			Bind:      config.PluginSet{Enabled: []config.Plugin{{Name: names.DefaultBinder}}},
			PreFilter: prefilter,
			Filter:    config.PluginSet{Enabled: []config.Plugin{{Name: name}}},
		},
		PluginConfig: s.pluginConfig,
	}
	opts := append([]frameworkruntime.Option{
		frameworkruntime.WithSnapshotSharedLister(lister),
	}, extraOpts...)
	fwk, err := frameworkruntime.NewFramework(ctx, s.registry, profile, opts...)
	if err != nil {
		return nil, fmt.Errorf("build framework for filter %s: %w", name, err)
	}
	return fwk, nil
}

func (s *frameworkSimulator) Simulate(ctx context.Context, in Input) (*SimulationResult, error) {
	if in.Pod == nil {
		return nil, fmt.Errorf("simulate: input pod is nil")
	}
	if in.Node == nil {
		return nil, fmt.Errorf("simulate: input node is nil")
	}

	// Full-cluster read-only snapshot: every node + every pod. PreFilter scans
	// the global SharedLister (InterPodAffinity / PodTopologySpread), so the
	// snapshot must hold the whole cluster (design §2.4). Defensively ensure the
	// target node is present even if the caller's AllNodes raced/paginated it out
	// — we are simulating against it, so its NodeInfo must exist.
	snapshot := newClusterSnapshot(ensureNodeInSnapshot(in.AllNodes, in.Node), in.AllPods)
	nodeInfo, err := snapshot.Get(in.Node.Name)
	if err != nil {
		return nil, fmt.Errorf("simulate: get node info for %s: %w", in.Node.Name, err)
	}

	// Every filter is driven on one fake-clientset informer factory: the volume
	// filters need PV/PVC/SC/CSINode listers, and InterPodAffinity /
	// PodTopologySpread need the Namespaces / Services / ReplicaSet listers from
	// h.SharedInformerFactory() — passing nil panics. The factory's list/watch
	// hit only the in-memory fake, never the real apiserver (design §2.6).
	sc := newSimContext(ctx, in)

	results := make([]FilterResult, 0, len(enabledFilters))
	for _, ef := range enabledFilters {
		results = append(results, s.runFilter(ctx, ef, snapshot, nodeInfo, in.Pod, sc))
	}

	res := &SimulationResult{
		Filters:             results,
		Outcome:             computeOutcome(results),
		SimulatedMinor:      SimulatedMinor,
		ClusterMinor:        in.ClusterMinor,
		SimulatedFeatureset: SimulatedFeatureset,
	}
	res.VersionMismatch = in.ClusterMinor != "" && in.ClusterMinor != SimulatedMinor
	res.LowConfidence = res.VersionMismatch
	return res, nil
}

// runFilter drives one plugin through PreFilter -> Filter and maps the
// framework.Status into a FilterResult per the design §1.4.3 mapping rules.
//
// Every framework is built with the fake-clientset informer factory + clientset
// (needed by InterPodAffinity/PodTopologySpread/volume plugins). For volume
// filters the static/dynamic gate and the volume-read-degrade gate are applied
// first (SKIPPED on either) before any framework is built (design §2.6.2).
func (s *frameworkSimulator) runFilter(ctx context.Context, ef enabledFilter, lister framework.SharedLister, nodeInfo *framework.NodeInfo, pod *corev1.Pod, sc *simContext) FilterResult {
	if ef.kind == kindVolume {
		if detail := sc.volumeSkipDetail(ef.name); detail != "" {
			return FilterResult{Filter: ef.name, Verdict: VerdictSkipped, SkipDetail: detail}
		}
	}

	fwk, err := s.newFrameworkForFilter(ctx, ef.name, lister,
		frameworkruntime.WithInformerFactory(sc.informers.factory),
		frameworkruntime.WithClientSet(sc.informers.client),
	)
	if err != nil {
		return FilterResult{Filter: ef.name, Verdict: VerdictSkipped, SkipDetail: fmt.Sprintf("engine error: %v", err)}
	}

	// Plugin New() registered its informers on the factory above; start and sync
	// them now (against the in-memory fake — never the real apiserver). A sync
	// failure demotes this filter to SKIPPED rather than risk a false PASS.
	if err := sc.informers.startAndSync(ctx); err != nil {
		return FilterResult{Filter: ef.name, Verdict: VerdictSkipped, SkipDetail: fmt.Sprintf("informer cache sync failed: %v", err)}
	}

	return s.drive(ctx, fwk, ef.name, nodeInfo, pod)
}

// drive runs PreFilter -> Filter on an already-built framework and maps the
// resulting status to a FilterResult.
func (s *frameworkSimulator) drive(ctx context.Context, fwk framework.Framework, name string, nodeInfo *framework.NodeInfo, pod *corev1.Pod) FilterResult {
	state := framework.NewCycleState()

	// PreFilter must run first to populate cycle state. Never skip it.
	_, preStatus, _ := fwk.RunPreFilterPlugins(ctx, state, pod)
	switch {
	case preStatus.IsSuccess():
		// proceed to Filter
	case preStatus.IsSkip():
		// PreFilter signalled this filter is irrelevant to the pod -> PASS short-circuit.
		return FilterResult{Filter: name, Verdict: VerdictPass}
	case preStatus.Code() == framework.Error:
		return FilterResult{Filter: name, Verdict: VerdictSkipped, SkipDetail: skipDetailForError(preStatus)}
	case preStatus.IsRejected():
		// Unschedulable / UnschedulableAndUnresolvable at PreFilter -> FAIL.
		return FilterResult{Filter: name, Verdict: VerdictFail, Reason: reasonFromStatus(preStatus)}
	default:
		return FilterResult{Filter: name, Verdict: VerdictSkipped, SkipDetail: skipDetailForError(preStatus)}
	}

	filterStatus := fwk.RunFilterPlugins(ctx, state, pod, nodeInfo)
	switch {
	case filterStatus.IsSuccess():
		return FilterResult{Filter: name, Verdict: VerdictPass}
	case filterStatus.Code() == framework.Error:
		return FilterResult{Filter: name, Verdict: VerdictSkipped, SkipDetail: skipDetailForError(filterStatus)}
	default:
		// Unschedulable / UnschedulableAndUnresolvable -> FAIL.
		return FilterResult{Filter: name, Verdict: VerdictFail, Reason: reasonFromStatus(filterStatus)}
	}
}
