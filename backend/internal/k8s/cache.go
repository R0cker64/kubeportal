// Package k8s keeps an informer-backed, read-only view of the workloads in
// the environment namespaces. It never filters by team: callers must pass
// every result through internal/authz.
package k8s

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/R0cker64/kubeportal/backend/internal/authz"
)

// Workload kinds exposed by the portal.
const (
	KindDeployment  = "deployment"
	KindStatefulSet = "statefulset"
)

// Workload is a Deployment or StatefulSet in an environment namespace.
type Workload struct {
	Kind      string
	Namespace string
	Name      string
	UID       types.UID
	// Labels are the workload's own labels; Team comes from them.
	Labels map[string]string
	Team   string
	// App is the pod template's app.kubernetes.io/name label ("" if absent).
	App       string
	Desired   int32
	Ready     int32
	Updated   int32
	CreatedAt time.Time
}

// Event is a Warning event about a workload or its pods.
type Event struct {
	Reason   string
	Message  string
	Object   string // kind/name of the involved object
	Count    int32
	LastSeen time.Time
}

// RESTConfig returns the in-cluster config, or the given kubeconfig with an
// explicit context (never the kubeconfig's current context).
func RESTConfig(kubeconfig, context string, timeout time.Duration) (*rest.Config, error) {
	var cfg *rest.Config
	var err error
	if kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: context},
		).ClientConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("k8s: config: %w", err)
	}
	cfg.Timeout = timeout
	cfg.UserAgent = "kubeportal"
	return cfg, nil
}

type nsListers struct {
	deployments  listersappsv1.DeploymentLister
	replicaSets  listersappsv1.ReplicaSetLister
	statefulSets listersappsv1.StatefulSetLister
	pods         listerscorev1.PodLister
	events       listerscorev1.EventLister
}

// Cache holds informers for the environment namespaces.
type Cache struct {
	log       *slog.Logger
	factories []informers.SharedInformerFactory
	listers   map[string]nsListers
	synced    []cache.InformerSynced

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewCache creates informers for the given namespaces. Call Start to run them.
func NewCache(client kubernetes.Interface, namespaces []string, log *slog.Logger) (*Cache, error) {
	c := &Cache{log: log, listers: map[string]nsListers{}, subs: map[chan struct{}]struct{}{}}
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { c.notify() },
		UpdateFunc: func(any, any) { c.notify() },
		DeleteFunc: func(any) { c.notify() },
	}

	for _, ns := range namespaces {
		f := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithNamespace(ns),
			informers.WithTransform(stripManagedFields))
		// Only Warning events are relevant; filter them server side.
		ev := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithNamespace(ns),
			informers.WithTransform(stripManagedFields),
			informers.WithTweakListOptions(func(o *metav1.ListOptions) { o.FieldSelector = "type=Warning" }))

		d := f.Apps().V1().Deployments()
		rs := f.Apps().V1().ReplicaSets()
		ss := f.Apps().V1().StatefulSets()
		p := f.Core().V1().Pods()
		e := ev.Core().V1().Events()

		for _, inf := range []cache.SharedIndexInformer{d.Informer(), rs.Informer(), ss.Informer(), p.Informer(), e.Informer()} {
			if _, err := inf.AddEventHandler(handler); err != nil {
				return nil, fmt.Errorf("k8s: add handler: %w", err)
			}
			c.synced = append(c.synced, inf.HasSynced)
		}
		c.listers[ns] = nsListers{
			deployments: d.Lister(), replicaSets: rs.Lister(), statefulSets: ss.Lister(),
			pods: p.Lister(), events: e.Lister(),
		}
		c.factories = append(c.factories, f, ev)
	}
	return c, nil
}

// stripManagedFields drops server-side apply bookkeeping, which the portal
// never reads, to keep the cache small.
func stripManagedFields(obj any) (any, error) {
	if a, err := meta.Accessor(obj); err == nil {
		a.SetManagedFields(nil)
	}
	return obj, nil
}

// Start runs the informers until ctx ends and waits for the initial sync,
// at most syncTimeout.
func (c *Cache) Start(ctx context.Context, syncTimeout time.Duration) error {
	for _, f := range c.factories {
		f.Start(ctx.Done())
	}
	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	if !cache.WaitForCacheSync(syncCtx.Done(), c.synced...) {
		return errors.New("k8s: informers did not sync in time")
	}
	return nil
}

// Synced reports whether every informer has completed its initial list.
func (c *Cache) Synced() bool {
	for _, s := range c.synced {
		if !s() {
			return false
		}
	}
	return true
}

// Subscribe returns a channel that receives a value after any change. The
// channel has a buffer of one, so bursts collapse. Call cancel when done.
func (c *Cache) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

func (c *Cache) notify() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Workloads returns every Deployment and StatefulSet, sorted by namespace,
// name and kind. Results are unfiltered: apply authz before returning them.
func (c *Cache) Workloads() []Workload {
	var out []Workload
	for _, l := range c.listers {
		deps, _ := l.deployments.List(labels.Everything())
		for _, d := range deps {
			out = append(out, fromDeployment(d))
		}
		sts, _ := l.statefulSets.List(labels.Everything())
		for _, s := range sts {
			out = append(out, fromStatefulSet(s))
		}
	}
	slices.SortFunc(out, func(a, b Workload) int {
		return cmp.Or(
			strings.Compare(a.Namespace, b.Namespace),
			strings.Compare(a.Name, b.Name),
			strings.Compare(a.Kind, b.Kind),
		)
	})
	return out
}

// Workload returns one workload. ok is false if it does not exist or the
// namespace is not an environment.
func (c *Cache) Workload(namespace, kind, name string) (Workload, bool) {
	l, ok := c.listers[namespace]
	if !ok {
		return Workload{}, false
	}
	switch kind {
	case KindDeployment:
		d, err := l.deployments.Deployments(namespace).Get(name)
		if err != nil {
			return Workload{}, false
		}
		return fromDeployment(d), true
	case KindStatefulSet:
		s, err := l.statefulSets.StatefulSets(namespace).Get(name)
		if err != nil {
			return Workload{}, false
		}
		return fromStatefulSet(s), true
	}
	return Workload{}, false
}

// Pods returns the pods controlled by w, following owner references (a
// Deployment owns ReplicaSets, which own pods), so a broad label selector
// can never pull in another workload's pods.
func (c *Cache) Pods(w Workload) []*corev1.Pod {
	l, ok := c.listers[w.Namespace]
	if !ok {
		return nil
	}
	owners := map[types.UID]bool{w.UID: true}
	if w.Kind == KindDeployment {
		rss, _ := l.replicaSets.ReplicaSets(w.Namespace).List(labels.Everything())
		for _, rs := range rss {
			if ref := metav1.GetControllerOf(rs); ref != nil && ref.UID == w.UID {
				owners[rs.UID] = true
			}
		}
		delete(owners, w.UID) // a Deployment never owns pods directly
	}
	pods, _ := l.pods.Pods(w.Namespace).List(labels.Everything())
	var out []*corev1.Pod
	for _, p := range pods {
		if ref := metav1.GetControllerOf(p); ref != nil && owners[ref.UID] {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Warnings returns the Warning events about w, its ReplicaSets or its pods,
// newest first, at most limit.
func (c *Cache) Warnings(w Workload, pods []*corev1.Pod, limit int) []Event {
	l, ok := c.listers[w.Namespace]
	if !ok {
		return nil
	}
	involved := map[string]bool{}
	switch w.Kind {
	case KindDeployment:
		involved["Deployment/"+w.Name] = true
		rss, _ := l.replicaSets.ReplicaSets(w.Namespace).List(labels.Everything())
		for _, rs := range rss {
			if ref := metav1.GetControllerOf(rs); ref != nil && ref.UID == w.UID {
				involved["ReplicaSet/"+rs.Name] = true
			}
		}
	case KindStatefulSet:
		involved["StatefulSet/"+w.Name] = true
	}
	for _, p := range pods {
		involved["Pod/"+p.Name] = true
	}

	evs, _ := l.events.Events(w.Namespace).List(labels.Everything())
	var out []Event
	for _, e := range evs {
		obj := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
		if e.Type != corev1.EventTypeWarning || !involved[obj] {
			continue
		}
		out = append(out, Event{
			Reason: e.Reason, Message: e.Message, Object: obj,
			Count: max(e.Count, 1), LastSeen: eventTime(e),
		})
	}
	slices.SortFunc(out, func(a, b Event) int { return b.LastSeen.Compare(a.LastSeen) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func eventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

func fromDeployment(d *appsv1.Deployment) Workload {
	return Workload{
		Kind: KindDeployment, Namespace: d.Namespace, Name: d.Name, UID: d.UID,
		Labels: d.Labels, Team: d.Labels[authz.TeamLabel], App: d.Spec.Template.Labels[authz.AppLabel],
		Desired: replicas(d.Spec.Replicas), Ready: d.Status.ReadyReplicas,
		Updated: d.Status.UpdatedReplicas, CreatedAt: d.CreationTimestamp.Time,
	}
}

func fromStatefulSet(s *appsv1.StatefulSet) Workload {
	return Workload{
		Kind: KindStatefulSet, Namespace: s.Namespace, Name: s.Name, UID: s.UID,
		Labels: s.Labels, Team: s.Labels[authz.TeamLabel], App: s.Spec.Template.Labels[authz.AppLabel],
		Desired: replicas(s.Spec.Replicas), Ready: s.Status.ReadyReplicas,
		Updated: s.Status.UpdatedReplicas, CreatedAt: s.CreationTimestamp.Time,
	}
}

func replicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}
