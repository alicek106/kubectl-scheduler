package simulate

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

// clusterSnapshot implements framework.SharedLister over the whole cluster
// (every node + every pod). It is the equivalent of the real scheduler's
// internal/cache.Snapshot, which we cannot import (internal/ package), so we
// build the same structure here on the public framework.SharedLister surface.
//
// [B7] The derived lists (nodes with affinity / required anti-affinity) and the
// PVC-used set are NOT built separately. After every AddPod has run, we make a
// single pass over the NodeInfo list and derive them from framework's own
// indexing (len(ni.PodsWithAffinity)>0, len(ni.PodsWithRequiredAntiAffinity)>0,
// the PVCRefCounts key union). This is exactly cache.Snapshot's procedure, so
// our HavePodsWith...List() / IsPVCUsedByPods() can never drift from the
// AddPod indexing — closing the false-PASS/FAIL trap structurally.
type clusterSnapshot struct {
	nodeInfoMap  map[string]*framework.NodeInfo // name -> NodeInfo (single source of truth)
	nodeInfoList []*framework.NodeInfo          // values of nodeInfoMap

	// Derived by traversing nodeInfoList after all AddPod calls (never built
	// separately). withAffinity and withAntiAff use DIFFERENT conditions and are
	// never merged into one list.
	withAffinity []*framework.NodeInfo
	withAntiAff  []*framework.NodeInfo
	pvcUsed      map[string]struct{} // key = "namespace/name"
}

var _ framework.SharedLister = (*clusterSnapshot)(nil)

// ensureNodeInSnapshot returns nodes with node appended if a node of that name
// is not already present. Used so the simulation target node always has a
// NodeInfo even if the caller's full list raced/paginated it out.
func ensureNodeInSnapshot(nodes []*corev1.Node, node *corev1.Node) []*corev1.Node {
	if node == nil {
		return nodes
	}
	for _, n := range nodes {
		if n.Name == node.Name {
			return nodes
		}
	}
	return append(nodes, node)
}

// newClusterSnapshot builds a read-only full-cluster snapshot from API types.
// allNodes is every node; allPods is every pod (across all namespaces). Pods are
// grouped onto their node by spec.nodeName; pods without a nodeName (unscheduled)
// or referencing an unknown node are ignored for placement (they are not running
// anywhere), matching the real cache which only holds pods assigned to a node.
func newClusterSnapshot(allNodes []*corev1.Node, allPods []*corev1.Pod) *clusterSnapshot {
	nodeInfoMap := make(map[string]*framework.NodeInfo, len(allNodes))
	for _, n := range allNodes {
		ni := framework.NewNodeInfo()
		ni.SetNode(n)
		nodeInfoMap[n.Name] = ni
	}

	for _, p := range allPods {
		if p.Spec.NodeName == "" {
			continue
		}
		ni, ok := nodeInfoMap[p.Spec.NodeName]
		if !ok {
			// Pod assigned to a node not in our node list (race / filtered out).
			// Skip placement; we cannot account it against an unknown node.
			continue
		}
		ni.AddPod(p)
	}

	nodeInfoList := make([]*framework.NodeInfo, 0, len(nodeInfoMap))
	for _, ni := range nodeInfoMap {
		nodeInfoList = append(nodeInfoList, ni)
	}

	s := &clusterSnapshot{
		nodeInfoMap:  nodeInfoMap,
		nodeInfoList: nodeInfoList,
		pvcUsed:      make(map[string]struct{}),
	}

	// Single derivation pass (B7): derive only from framework's own indexing.
	for _, ni := range nodeInfoList {
		if len(ni.PodsWithAffinity) > 0 {
			s.withAffinity = append(s.withAffinity, ni)
		}
		if len(ni.PodsWithRequiredAntiAffinity) > 0 {
			s.withAntiAff = append(s.withAntiAff, ni)
		}
		for key := range ni.PVCRefCounts {
			s.pvcUsed[key] = struct{}{}
		}
	}
	return s
}

func (s *clusterSnapshot) NodeInfos() framework.NodeInfoLister       { return s }
func (s *clusterSnapshot) StorageInfos() framework.StorageInfoLister { return s }

// NodeInfoLister

func (s *clusterSnapshot) List() ([]*framework.NodeInfo, error) {
	return s.nodeInfoList, nil
}

func (s *clusterSnapshot) HavePodsWithAffinityList() ([]*framework.NodeInfo, error) {
	return s.withAffinity, nil
}

func (s *clusterSnapshot) HavePodsWithRequiredAntiAffinityList() ([]*framework.NodeInfo, error) {
	return s.withAntiAff, nil
}

func (s *clusterSnapshot) Get(nodeName string) (*framework.NodeInfo, error) {
	if ni, ok := s.nodeInfoMap[nodeName]; ok {
		return ni, nil
	}
	return nil, fmt.Errorf("nodeinfo not found for node name %q", nodeName)
}

// StorageInfoLister

// IsPVCUsedByPods reports whether any scheduled pod references the PVC, derived
// from the PVCRefCounts key union (key = "namespace/name", framework's
// GetNamespacedName format). Real value, never false-fixed.
func (s *clusterSnapshot) IsPVCUsedByPods(key string) bool {
	_, ok := s.pvcUsed[key]
	return ok
}
