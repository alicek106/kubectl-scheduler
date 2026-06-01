package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/alicek106/kubectl-scheduler/internal/simulate"
)

// stdout/stderr are indirected so tests can capture output. Results go to
// stdout; diagnostics/warnings go to stderr.
func stdout() io.Writer { return os.Stdout }
func stderr() io.Writer { return os.Stderr }

// honestFooter is appended to every result. It blocks any "100% identical to
// your cluster's scheduler" reading at the output layer (design §4.5.1,
// requirements "정직한 출력").
const honestFooter = "Best-effort; NOT identical to your cluster. " +
	"Score/preemption/feature-gates out of scope."

// gateNote states the RBAC limitation that the real --feature-gates cannot be
// detected (design §1.5.1). Kept as a constant so the test can assert it.
const gateNote = "Note: the cluster's real --feature-gates cannot be read over RBAC, " +
	"so gate-driven differences are not detectable."

// sectionRule is the horizontal divider before the version banner.
const sectionRule = "──────────────────────────────────"

// gridColumns is how many PASS filters are laid out per row in the grid.
const gridColumns = 3

// RenderOptions carries the headline context (which simulate.SimulationResult
// intentionally does not know about) and the color setting. simulate stays
// unchanged; the cli layer supplies pod/node/namespace.
type RenderOptions struct {
	PodName   string
	NodeName  string
	Namespace string
	Color     bool
}

// colorEnabled reports whether ANSI color should be emitted. Color is on only
// when w is a real terminal AND NO_COLOR is unset AND --no-color was not passed.
// Non-TTY (pipe/redirect) output is always plain so it stays grep-friendly.
func colorEnabled(w io.Writer, noColorFlag bool) bool {
	if noColorFlag {
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// ANSI helpers. Kept tiny and self-contained (no external color library). When
// the palette is disabled every wrapper is the identity function, so callers do
// not need to branch — the gating happens once, here.
type palette struct{ on bool }

func (p palette) wrap(code, s string) string {
	if !p.on {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p palette) green(s string) string  { return p.wrap("32", s) }
func (p palette) red(s string) string    { return p.wrap("31", s) }
func (p palette) yellow(s string) string { return p.wrap("33", s) }
func (p palette) dim(s string) string    { return p.wrap("2", s) }
func (p palette) bold(s string) string   { return p.wrap("1", s) }

// render writes a human-readable, honest view of the simulation result in the
// approved "section + symbol" layout. Every run shows: a 3-state headline (never
// over-asserting), the checked filters, the SKIPPED filters with reasons, the
// version banner, and the fixed footer (design §4.5). Formatting lives here
// (cli) only; simulate produces pure data.
func render(w io.Writer, r *simulate.SimulationResult, opts RenderOptions) error {
	p := palette{on: opts.Color}

	writeHeadline(w, p, r, opts)
	writeCheckedFilters(w, p, r)
	writeSkippedFilters(w, p, r)
	writeSummary(w, p, r)
	writeBanner(w, p, r)
	return nil
}

// headlineContext renders the "<pod> → <node>  (namespace: <ns>)" suffix.
func headlineContext(opts RenderOptions) string {
	return fmt.Sprintf("%s → %s  (namespace: %s)", opts.PodName, opts.NodeName, opts.Namespace)
}

// writeHeadline maps the 3-state Outcome to an honest one-line headline. The
// PASS_WITH_UNCHECKED case never asserts "Schedulable" (design §4.5.2).
func writeHeadline(w io.Writer, p palette, r *simulate.SimulationResult, opts RenderOptions) {
	ctx := headlineContext(opts)
	switch r.Outcome {
	case simulate.OutcomePass:
		fmt.Fprintln(w, p.bold(p.green("✓ SCHEDULABLE"))+"   "+ctx)
	case simulate.OutcomePassWithUnchecked:
		fmt.Fprintln(w, p.bold(p.yellow("⊘ PASSED (with unchecked)"))+"   "+ctx)
	case simulate.OutcomeFail:
		fmt.Fprintln(w, p.bold(p.red("✗ BLOCKED"))+"   "+ctx)
	default:
		fmt.Fprintln(w, p.bold(string(r.Outcome))+"   "+ctx)
	}
}

// writeCheckedFilters lists the filters that were actually judged (design
// §4.5.1 (a)). PASS filters are laid out in a sorted multi-column grid; FAIL
// filters get a one-line "✗ <name>   <reason>".
func writeCheckedFilters(w io.Writer, p palette, r *simulate.SimulationResult) {
	var passed []string
	var failed []simulate.FilterResult
	for _, f := range r.Filters {
		switch f.Verdict {
		case simulate.VerdictPass:
			passed = append(passed, f.Filter)
		case simulate.VerdictFail:
			failed = append(failed, f)
		}
	}

	fmt.Fprintln(w, "\nChecked filters")
	if len(passed) == 0 && len(failed) == 0 {
		fmt.Fprintln(w, "  (none)")
		return
	}

	writePassGrid(w, p, passed)
	for _, f := range failed {
		fmt.Fprintf(w, "  %s %s   %s\n", p.red("✗"), f.Filter, f.Reason)
	}
}

// writePassGrid prints PASS filter names sorted, in a fixed-column grid with the
// name column padded to the widest entry so columns align.
func writePassGrid(w io.Writer, p palette, names []string) {
	if len(names) == 0 {
		return
	}
	sort.Strings(names)

	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}

	check := p.green("✓")
	for i := 0; i < len(names); i += gridColumns {
		var b strings.Builder
		b.WriteString("  ")
		for j := i; j < i+gridColumns && j < len(names); j++ {
			cell := fmt.Sprintf("%s %-*s", check, width, names[j])
			b.WriteString(cell)
			if j+1 < i+gridColumns && j+1 < len(names) {
				b.WriteString("   ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// writeSkippedFilters lists the SKIPPED filters with their reasons so the user
// always sees what was NOT checked (design §4.5.1 (b)). Only printed when there
// is at least one SKIPPED filter.
func writeSkippedFilters(w io.Writer, p palette, r *simulate.SimulationResult) {
	var skipped []simulate.FilterResult
	for _, f := range r.Filters {
		if f.Verdict == simulate.VerdictSkipped {
			skipped = append(skipped, f)
		}
	}
	if len(skipped) == 0 {
		return
	}
	fmt.Fprintf(w, "\nNot checked (%d)\n", len(skipped))
	for _, f := range skipped {
		fmt.Fprintf(w, "  %s %s   %s\n", p.yellow("⊘"), f.Filter, f.SkipDetail)
	}
}

// writeSummary prints the one-line "N passed · N failed · N skipped" tally.
func writeSummary(w io.Writer, p palette, r *simulate.SimulationResult) {
	var pass, fail, skip int
	for _, f := range r.Filters {
		switch f.Verdict {
		case simulate.VerdictPass:
			pass++
		case simulate.VerdictFail:
			fail++
		case simulate.VerdictSkipped:
			skip++
		}
	}
	fmt.Fprintf(w, "\nSummary  %d passed · %d failed · %d skipped\n",
		pass, fail, skip)
}

// writeBanner prints the divider, the version banner (simulated minor +
// featureset, cluster minor, mismatch warning, low-confidence marker, the
// feature-gate detection limitation), and the fixed honest footer
// (design §4.5.1 (c), §1.5.1).
func writeBanner(w io.Writer, p palette, r *simulate.SimulationResult) {
	cluster := r.ClusterMinor
	if cluster == "" {
		cluster = "unknown"
	}

	fmt.Fprintln(w, p.dim(sectionRule))
	fmt.Fprintln(w, p.dim(fmt.Sprintf(
		"Simulated: kube-scheduler v%s logic · featureset %s · cluster v%s",
		r.SimulatedMinor, r.SimulatedFeatureset, cluster)))

	if r.VersionMismatch {
		fmt.Fprintln(w, p.yellow(fmt.Sprintf(
			"WARNING: simulated minor (v%s) differs from cluster minor (v%s) — results may differ.",
			r.SimulatedMinor, cluster)))
	}
	if r.LowConfidence {
		fmt.Fprintln(w, p.yellow("WARNING: LOW-CONFIDENCE — version mismatch and/or feature-gate differences may change the real scheduler's decision."))
	}

	fmt.Fprintln(w, p.dim(gateNote))
	fmt.Fprintln(w, p.dim("ⓘ "+honestFooter))
}
