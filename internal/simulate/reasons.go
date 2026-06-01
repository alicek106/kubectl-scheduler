package simulate

import (
	"fmt"
	"strings"

	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// reasonFromStatus turns a framework.Status into a human-readable reason
// sentence. It prefers the plugin's own Reasons(), falling back to the status
// message.
func reasonFromStatus(status *framework.Status) string {
	if status == nil {
		return ""
	}
	reasons := status.Reasons()
	if len(reasons) > 0 {
		return strings.Join(reasons, "; ")
	}
	if msg := status.Message(); msg != "" {
		return msg
	}
	return status.Code().String()
}

// skipDetailForError describes why a filter could not be checked when its
// PreFilter/Filter returned an internal Error (engine-internal, not a normal
// "unschedulable"). Kept distinct from cluster-wide SKIPPED reasons so the user
// can tell "engine error" from "requires cluster-wide state" (review note).
func skipDetailForError(status *framework.Status) string {
	return fmt.Sprintf("engine error: %s", reasonFromStatus(status))
}
