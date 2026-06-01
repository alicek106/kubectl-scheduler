// Package cli is the thin command layer: cobra wiring, flags, kubeconfig
// resolution, and output formatting. Business logic lives in internal/kube
// (read-only cluster access) and internal/simulate (the scheduling engine).
package cli

import (
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

// defaultTimeout bounds each run's cluster reads to avoid hanging. Raised to
// 30s for the full Node/Pod (+ volume) reads of the cluster snapshot (§3.2.1-3).
const defaultTimeout = 30 * time.Second

// NewRootCommand builds the `kubectl schedule <pod> <node>` command.
func NewRootCommand() *cobra.Command {
	configFlags := genericclioptions.NewConfigFlags(true)
	var noColor bool

	cmd := &cobra.Command{
		Use:   "schedule <pod> <node>",
		Short: "Simulate whether a pod can be scheduled onto a node (read-only)",
		Long: "kubectl-schedule simulates whether the given pod could be scheduled onto the\n" +
			"given node in the current cluster, using the real kube-scheduler filter\n" +
			"plugins. It is completely read-only: it never creates, updates, deletes, or\n" +
			"binds anything.",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), configFlags, args[0], args[1], noColor)
		},
	}

	cmd.Flags().BoolVar(&noColor, "no-color", false, "disable ANSI color output (also disabled when stdout is not a terminal or NO_COLOR is set)")

	// Registers --kubeconfig, --context, --namespace/-n, etc.
	configFlags.AddFlags(cmd.PersistentFlags())
	return cmd
}
