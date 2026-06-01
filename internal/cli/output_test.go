package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alicek106/kubectl-scheduler/internal/simulate"
)

// renderTo renders into a buffer for assertions, with color OFF (plain text) so
// substring assertions are stable regardless of TTY.
func renderTo(t *testing.T, r *simulate.SimulationResult) string {
	t.Helper()
	return renderWith(t, r, RenderOptions{
		PodName:   "nginx",
		NodeName:  "worker-2",
		Namespace: "default",
		Color:     false,
	})
}

func renderWith(t *testing.T, r *simulate.SimulationResult, opts RenderOptions) string {
	t.Helper()
	var buf bytes.Buffer
	if err := render(&buf, r, opts); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// TestRender_AlwaysHasThreeElementsAndFooter: every output, regardless of
// Outcome, includes (a) checked filters, (b) the version banner, and the fixed
// honest footer (design §4.5.1). Also asserts the headline context.
func TestRender_AlwaysHasThreeElementsAndFooter(t *testing.T) {
	r := &simulate.SimulationResult{
		Outcome:             simulate.OutcomePass,
		Filters:             []simulate.FilterResult{{Filter: "NodeAffinity", Verdict: simulate.VerdictPass}},
		SimulatedMinor:      "1.32",
		ClusterMinor:        "1.32",
		SimulatedFeatureset: "default@v1.32",
	}
	out := renderTo(t, r)
	for _, want := range []string{
		"✓ SCHEDULABLE",
		"nginx → worker-2",
		"(namespace: default)",
		"Checked filters",
		"✓ NodeAffinity",
		"Summary  1 passed · 0 failed · 0 skipped",
		"Simulated: kube-scheduler v1.32 logic",
		"featureset default@v1.32",
		"cluster v1.32",
		"NOT identical to your cluster",
		"out of scope",
		"--feature-gates cannot be read over RBAC",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRender_PassWithUnchecked_NeverAssertsSchedulable is the false-PASS
// regression gate: when any filter is SKIPPED the headline must NOT claim
// "Schedulable", and the SKIPPED filter + its reason must be surfaced
// (design §4.5.2).
func TestRender_PassWithUnchecked_NeverAssertsSchedulable(t *testing.T) {
	r := &simulate.SimulationResult{
		Outcome: simulate.OutcomePassWithUnchecked,
		Filters: []simulate.FilterResult{
			{Filter: "NodeResourcesFit", Verdict: simulate.VerdictPass},
			{Filter: "VolumeBinding", Verdict: simulate.VerdictSkipped, SkipDetail: "dynamic provisioning (WaitForFirstConsumer) decided at runtime"},
		},
		SimulatedMinor:      "1.32",
		ClusterMinor:        "1.32",
		SimulatedFeatureset: "default@v1.32",
	}
	out := renderTo(t, r)

	if strings.Contains(out, "SCHEDULABLE") || strings.Contains(out, "Schedulable") {
		t.Errorf("PASS_WITH_UNCHECKED headline must not assert 'Schedulable'\n---\n%s", out)
	}
	for _, want := range []string{
		"⊘ PASSED (with unchecked)",
		"Not checked (1)",
		"⊘ VolumeBinding",
		"dynamic provisioning (WaitForFirstConsumer) decided at runtime",
		"Summary  1 passed · 0 failed · 1 skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRender_Fail_ShowsReason: a FAIL headline + the per-filter FAIL line name
// the blocking filter and its reason (design §4.5.2).
func TestRender_Fail_ShowsReason(t *testing.T) {
	r := &simulate.SimulationResult{
		Outcome: simulate.OutcomeFail,
		Filters: []simulate.FilterResult{
			{Filter: "NodeResourcesFit", Verdict: simulate.VerdictPass},
			{Filter: "TaintToleration", Verdict: simulate.VerdictFail, Reason: "node had untolerated taint {dedicated=gpu:NoSchedule}"},
		},
		SimulatedMinor:      "1.32",
		ClusterMinor:        "1.32",
		SimulatedFeatureset: "default@v1.32",
	}
	out := renderTo(t, r)
	for _, want := range []string{
		"✗ BLOCKED",
		"✗ TaintToleration",
		"untolerated taint",
		"Summary  1 passed · 1 failed · 0 skipped",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRender_VersionMismatch_WarnsAndLowConfidence: a minor mismatch produces a
// warning and a LOW-CONFIDENCE marker, plus the gate-detection-limit note
// (design §4.5.1 (c), §1.5.1).
func TestRender_VersionMismatch_WarnsAndLowConfidence(t *testing.T) {
	r := &simulate.SimulationResult{
		Outcome:             simulate.OutcomePass,
		Filters:             []simulate.FilterResult{{Filter: "NodeName", Verdict: simulate.VerdictPass}},
		SimulatedMinor:      "1.32",
		ClusterMinor:        "1.30",
		SimulatedFeatureset: "default@v1.32",
		VersionMismatch:     true,
		LowConfidence:       true,
	}
	out := renderTo(t, r)
	for _, want := range []string{
		"WARNING",
		"differs from cluster minor",
		"LOW-CONFIDENCE",
		"--feature-gates cannot be read over RBAC",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRender_NoColor_HasNoANSIEscapes: with Color=false the output must contain
// no ANSI escape sequences (pipe/redirect/NO_COLOR/--no-color path), but must
// still keep the symbols and text.
func TestRender_NoColor_HasNoANSIEscapes(t *testing.T) {
	r := &simulate.SimulationResult{
		Outcome: simulate.OutcomeFail,
		Filters: []simulate.FilterResult{
			{Filter: "NodeAffinity", Verdict: simulate.VerdictPass},
			{Filter: "TaintToleration", Verdict: simulate.VerdictFail, Reason: "untolerated taint"},
			{Filter: "VolumeBinding", Verdict: simulate.VerdictSkipped, SkipDetail: "runtime"},
		},
		SimulatedMinor:      "1.32",
		ClusterMinor:        "1.30",
		SimulatedFeatureset: "default@v1.32",
		VersionMismatch:     true,
		LowConfidence:       true,
	}
	out := renderWith(t, r, RenderOptions{PodName: "p", NodeName: "n", Namespace: "ns", Color: false})

	if strings.Contains(out, "\x1b[") {
		t.Errorf("color-disabled output must contain no ANSI escapes\n---\n%q", out)
	}
	// Symbols and text are still present.
	for _, want := range []string{"✗ BLOCKED", "✓ NodeAffinity", "⊘ VolumeBinding"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

// TestRender_Color_HasANSIEscapes: with Color=true the output contains ANSI
// escape sequences for the colored elements.
func TestRender_Color_HasANSIEscapes(t *testing.T) {
	r := &simulate.SimulationResult{
		Outcome:             simulate.OutcomePass,
		Filters:             []simulate.FilterResult{{Filter: "NodeAffinity", Verdict: simulate.VerdictPass}},
		SimulatedMinor:      "1.32",
		ClusterMinor:        "1.32",
		SimulatedFeatureset: "default@v1.32",
	}
	out := renderWith(t, r, RenderOptions{PodName: "p", NodeName: "n", Namespace: "ns", Color: true})

	if !strings.Contains(out, "\x1b[") {
		t.Errorf("color-enabled output must contain ANSI escapes\n---\n%q", out)
	}
}
