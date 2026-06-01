package simulate

import (
	"context"
	"testing"
)

func TestComputeOutcome(t *testing.T) {
	tests := []struct {
		name    string
		filters []FilterResult
		want    Outcome
	}{
		{"all pass", []FilterResult{{Verdict: VerdictPass}, {Verdict: VerdictPass}}, OutcomePass},
		{"one fail", []FilterResult{{Verdict: VerdictPass}, {Verdict: VerdictFail}}, OutcomeFail},
		{"pass with skipped", []FilterResult{{Verdict: VerdictPass}, {Verdict: VerdictSkipped}}, OutcomePassWithUnchecked},
		{"fail beats skipped", []FilterResult{{Verdict: VerdictSkipped}, {Verdict: VerdictFail}}, OutcomeFail},
		{"empty", nil, OutcomePass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeOutcome(tt.filters); got != tt.want {
				t.Errorf("computeOutcome = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSimulate_VersionMetadata(t *testing.T) {
	sim := NewFrameworkSimulator()
	res, err := sim.Simulate(context.Background(), Input{
		Pod:          podRequesting("100m", "100Mi"),
		Node:         node("n1"),
		ClusterMinor: "1.30",
	})
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if res.SimulatedMinor != SimulatedMinor {
		t.Errorf("SimulatedMinor = %q, want %q", res.SimulatedMinor, SimulatedMinor)
	}
	if !res.VersionMismatch {
		t.Errorf("expected VersionMismatch with cluster 1.30 vs simulated %s", SimulatedMinor)
	}
	if !res.LowConfidence {
		t.Errorf("expected LowConfidence on version mismatch")
	}
	if res.SimulatedFeatureset != SimulatedFeatureset {
		t.Errorf("SimulatedFeatureset = %q, want %q", res.SimulatedFeatureset, SimulatedFeatureset)
	}
}

func TestSimulate_NilInput(t *testing.T) {
	sim := NewFrameworkSimulator()
	if _, err := sim.Simulate(context.Background(), Input{Node: node("n1")}); err == nil {
		t.Error("expected error on nil pod")
	}
	if _, err := sim.Simulate(context.Background(), Input{Pod: podRequesting("1", "1Gi")}); err == nil {
		t.Error("expected error on nil node")
	}
}

func TestSimulate_AllT1FiltersPass(t *testing.T) {
	sim := NewFrameworkSimulator()
	res, err := sim.Simulate(context.Background(), Input{
		Pod:          podRequesting("100m", "100Mi"),
		Node:         node("n1"),
		ClusterMinor: SimulatedMinor,
	})
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if res.Outcome != OutcomePass {
		t.Errorf("Outcome = %s, want PASS; filters: %+v", res.Outcome, res.Filters)
	}
	if len(res.Filters) != len(enabledFilters) {
		t.Errorf("got %d filter results, want %d", len(res.Filters), len(enabledFilters))
	}
}
