package kube

import (
	"context"
	"errors"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestGetPod_NotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	r := NewReader(cs)
	_, err := r.GetPod(context.Background(), "default", "missing")
	if !errors.Is(err, ErrPodNotFound) {
		t.Errorf("got %v, want ErrPodNotFound", err)
	}
}

func TestGetNode_NotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	r := NewReader(cs)
	_, err := r.GetNode(context.Background(), "missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("got %v, want ErrNodeNotFound", err)
	}
}

func TestGetPod_OK(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"}}
	cs := fake.NewSimpleClientset(pod)
	r := NewReader(cs)
	got, err := r.GetPod(context.Background(), "default", "p")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "p" {
		t.Errorf("got pod %q, want p", got.Name)
	}
}

// TestListAllNodes_RV0AndPagination verifies the full node list applies RV="0"
// + Limit, follows the continue token across pages, and returns every node.
func TestListAllNodes_RV0AndPagination(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n2"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n3"}},
	)

	// Simulate paged responses: return one item per call with a continue token
	// until exhausted, and assert RV="0" + Limit on every call.
	calls := 0
	allNames := []string{"n1", "n2", "n3"}
	cs.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		opts := action.(k8stesting.ListActionImpl).ListOptions
		if opts.ResourceVersion != "0" {
			t.Errorf("expected ResourceVersion=0, got %q", opts.ResourceVersion)
		}
		if opts.Limit != listPageLimit {
			t.Errorf("expected Limit=%d, got %d", listPageLimit, opts.Limit)
		}
		idx := calls
		calls++
		list := &corev1.NodeList{}
		if idx < len(allNames) {
			list.Items = []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: allNames[idx]}}}
		}
		if idx < len(allNames)-1 {
			list.Continue = fmt.Sprintf("page-%d", idx+1)
		}
		return true, list, nil
	})

	r := NewReader(cs)
	nodes, err := r.ListAllNodes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nodes) != 3 {
		t.Errorf("got %d nodes, want 3", len(nodes))
	}
	if calls < 3 {
		t.Errorf("expected at least 3 paged calls, got %d", calls)
	}
}

func TestListAllPods_OK(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns2"}},
	)
	r := NewReader(cs)
	pods, err := r.ListAllPods(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("got %d pods, want 2", len(pods))
	}
}

func TestListAllNodes_ForbiddenDegrades(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("no list"))
	})
	r := NewReader(cs)
	_, err := r.ListAllNodes(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("got %v, want ErrForbidden", err)
	}
}

func TestListAllPods_ForbiddenDegrades(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("no list"))
	})
	r := NewReader(cs)
	_, err := r.ListAllPods(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("got %v, want ErrForbidden", err)
	}
}

func TestListCSINodesAndStorageClasses_OK(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "standard"}},
	)
	r := NewReader(cs)
	csiNodes, err := r.ListCSINodes(context.Background())
	if err != nil || len(csiNodes) != 1 {
		t.Fatalf("ListCSINodes: got %d (%v), want 1", len(csiNodes), err)
	}
	scs, err := r.ListStorageClasses(context.Background())
	if err != nil || len(scs) != 1 {
		t.Fatalf("ListStorageClasses: got %d (%v), want 1", len(scs), err)
	}
}

func TestListStorageClasses_ForbiddenDegrades(t *testing.T) {
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("list", "storageclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "storageclasses"}, "", errors.New("no list"))
	})
	r := NewReader(cs)
	_, err := r.ListStorageClasses(context.Background())
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("got %v, want ErrForbidden", err)
	}
}

func TestGetPVCAndPV_OK(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "pvc", Namespace: "default"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv"}},
		&corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "pv"}},
	)
	r := NewReader(cs)
	pvc, err := r.GetPVC(context.Background(), "default", "pvc")
	if err != nil || pvc.Spec.VolumeName != "pv" {
		t.Fatalf("GetPVC: got %v (%v)", pvc, err)
	}
	pv, err := r.GetPV(context.Background(), "pv")
	if err != nil || pv.Name != "pv" {
		t.Fatalf("GetPV: got %v (%v)", pv, err)
	}
}
