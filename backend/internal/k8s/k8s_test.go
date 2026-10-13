package k8s

import (
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr[T any](v T) *T { return &v }

func controller(kind, name string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, UID: uid, Controller: ptr(true)}}
}

func deployment(ns, name, team string, uid types.UID) *appsv1.Deployment {
	l := map[string]string{"app.kubernetes.io/name": name}
	if team != "" {
		l["team"] = team
	}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: uid, Labels: l},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(int32(1)),
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: l}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1, UpdatedReplicas: 1},
	}
}

func newTestCache(t *testing.T, objs ...runtime.Object) *Cache {
	t.Helper()
	c, err := NewCache(fake.NewClientset(objs...), []string{"dev", "prod"},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCacheOwnershipAndNamespaces(t *testing.T) {
	objs := []runtime.Object{
		deployment("prod", "payments-api", "payments", "d1"),
		deployment("prod", "catalog-api", "catalog", "d2"),
		// Not an environment namespace: must be invisible.
		deployment("kube-system", "coredns", "", "d3"),
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "payments-api-abc", UID: "rs1",
			OwnerReferences: controller("Deployment", "payments-api", "d1"),
		}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "catalog-api-xyz", UID: "rs2",
			OwnerReferences: controller("Deployment", "catalog-api", "d2"),
		}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "payments-api-abc-1",
			Labels:          map[string]string{"app.kubernetes.io/name": "payments-api"},
			OwnerReferences: controller("ReplicaSet", "payments-api-abc", "rs1"),
		}},
		// Same app label, different owner: must not be attributed to payments-api.
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "catalog-api-xyz-1",
			Labels:          map[string]string{"app.kubernetes.io/name": "payments-api"},
			OwnerReferences: controller("ReplicaSet", "catalog-api-xyz", "rs2"),
		}},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "prod", Name: "e1"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "payments-api-abc-1"},
			Type:           corev1.EventTypeWarning, Reason: "BackOff", Message: "restarting",
			LastTimestamp: metav1.NewTime(time.Now()),
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "prod", Name: "e2"},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Name: "catalog-api-xyz-1"},
			Type:           corev1.EventTypeWarning, Reason: "BackOff",
		},
	}
	c := newTestCache(t, objs...)

	var names []string
	for _, w := range c.Workloads() {
		names = append(names, w.Namespace+"/"+w.Name)
	}
	if !slices.Equal(names, []string{"prod/catalog-api", "prod/payments-api"}) {
		t.Fatalf("Workloads = %v", names)
	}
	if _, ok := c.Workload("kube-system", KindDeployment, "coredns"); ok {
		t.Fatal("workload outside environment namespaces is reachable")
	}
	if _, ok := c.Workload("prod", KindStatefulSet, "payments-api"); ok {
		t.Fatal("kind is not part of the lookup")
	}

	w, ok := c.Workload("prod", KindDeployment, "payments-api")
	if !ok || w.Team != "payments" || w.App != "payments-api" {
		t.Fatalf("Workload = %+v, %v", w, ok)
	}
	pods := c.Pods(w)
	if len(pods) != 1 || pods[0].Name != "payments-api-abc-1" {
		t.Fatalf("Pods = %v", pods)
	}
	evs := c.Warnings(w, pods, 10)
	if len(evs) != 1 || evs[0].Object != "Pod/payments-api-abc-1" {
		t.Fatalf("Warnings = %+v", evs)
	}
}

func TestSubscribeNotifiesChanges(t *testing.T) {
	client := fake.NewClientset()
	c, err := NewCache(client, []string{"dev"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ch, cancel := c.Subscribe()
	defer cancel()
	if err := c.Start(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppsV1().Deployments("dev").Create(t.Context(),
		deployment("dev", "x", "payments", "u"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no notification after a change")
	}
}

func TestAssess(t *testing.T) {
	now := time.Now()
	w := Workload{Desired: 2, Ready: 2, Updated: 2}
	running := func(name string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
			Status: corev1.PodStatus{
				Phase:             corev1.PodRunning,
				Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}},
			},
		}
	}

	h, infos := Assess(w, []*corev1.Pod{running("a"), running("b")}, now)
	if h.Status != StatusHealthy || len(h.Reasons) != 0 || len(infos) != 2 || !infos[0].Ready {
		t.Fatalf("healthy: %+v %+v", h, infos)
	}

	crashing := running("c")
	crashing.Status.ContainerStatuses[0] = corev1.ContainerStatus{
		Name: "app", RestartCount: 4,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", FinishedAt: metav1.NewTime(now.Add(-time.Minute)),
		}},
	}
	h, _ = Assess(Workload{Desired: 2, Ready: 1, Updated: 2}, []*corev1.Pod{running("a"), crashing}, now)
	if h.Status != StatusDegraded || h.Restarts != 4 ||
		!slices.Equal(h.Reasons, []string{ReasonCrashLoopBackOff, ReasonOOMKilled, ReasonReplicasNotReady}) {
		t.Fatalf("degraded: %+v", h)
	}

	h, _ = Assess(Workload{Desired: 1, Ready: 0, Updated: 1}, []*corev1.Pod{crashing}, now)
	if h.Status != StatusDown || !slices.Contains(h.Reasons, ReasonNoReadyReplicas) {
		t.Fatalf("down: %+v", h)
	}

	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", CreationTimestamp: metav1.NewTime(now.Add(-10 * time.Minute))},
		Status:     corev1.PodStatus{Phase: corev1.PodPending},
	}
	h, _ = Assess(Workload{Desired: 2, Ready: 2, Updated: 1}, []*corev1.Pod{running("a"), pending}, now)
	if h.Status != StatusDegraded ||
		!slices.Equal(h.Reasons, []string{ReasonPendingTooLong, ReasonRolloutInProgress}) {
		t.Fatalf("pending: %+v", h)
	}

	h, _ = Assess(Workload{Desired: 0}, nil, now)
	if h.Status != StatusHealthy {
		t.Fatalf("scaled to zero: %+v", h)
	}
}
