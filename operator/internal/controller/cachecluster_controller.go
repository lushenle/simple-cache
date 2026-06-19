package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	cachev1 "github.com/lushenle/simple-cache/operator/api/v1"
)

const finalizerName = "cache.shenle.lu/finalizer"

// CacheClusterReconciler reconciles a CacheCluster object
type CacheClusterReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cache.shenle.lu,resources=cacheclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cache.shenle.lu,resources=cacheclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cache.shenle.lu,resources=cacheclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch;delete

func (r *CacheClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	reconcileTotal.Inc()
	timer := prometheus.NewTimer(reconcileDuration)
	defer timer.ObserveDuration()

	// Fetch CacheCluster
	cr := &cachev1.CacheCluster{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Validate spec (includes Secret existence check)
	if err := r.validateSpec(ctx, cr); err != nil {
		logger.Error(err, "spec validation failed")
		reconcileErrors.Inc()
		r.Recorder.Event(cr, corev1.EventTypeWarning, "ValidationFailed", err.Error())
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !cr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, cr)
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(cr, finalizerName) {
		controllerutil.AddFinalizer(cr, finalizerName)
		if err := r.Update(ctx, cr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile child resources
	if err := r.reconcileResources(ctx, cr); err != nil {
		reconcileErrors.Inc()
		return ctrl.Result{}, err
	}

	// Update status
	if err := r.updateStatus(ctx, cr); err != nil {
		reconcileErrors.Inc()
		return ctrl.Result{}, err
	}

	// Update managed cluster/node gauges from the latest status
	latest := &cachev1.CacheCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, latest); err == nil {
		managedNodes.Set(float64(latest.Status.ReadyReplicas))
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *CacheClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1.CacheCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToCluster),
			builder.WithPredicates(podStatusChangePredicate()),
		).
		Complete(r)
}

func (r *CacheClusterReconciler) reconcileResources(ctx context.Context, cr *cachev1.CacheCluster) error {
	logger := log.FromContext(ctx)

	// 1. Headless Service
	headlessSvc := buildHeadlessService(cr)
	if err := r.createOrUpdate(ctx, cr, headlessSvc); err != nil {
		return fmt.Errorf("headless service: %w", err)
	}

	// 2. Client Service
	clientSvc := buildClientService(cr)
	if err := r.createOrUpdate(ctx, cr, clientSvc); err != nil {
		return fmt.Errorf("client service: %w", err)
	}

	// 3. ConfigMap
	cm := buildConfigMap(cr)
	if err := r.createOrUpdate(ctx, cr, cm); err != nil {
		return fmt.Errorf("configmap: %w", err)
	}

	// 4. StatefulSet — with graceful scale-down support
	sts, err := buildStatefulSet(cr)
	if err != nil {
		return fmt.Errorf("statefulset: %w", err)
	}

	// If scaling down, gracefully remove nodes from Raft cluster first
	desiredReplicas := cr.Spec.Replicas
	existingSts := &appsv1.StatefulSet{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), existingSts); err == nil && existingSts.Spec.Replicas != nil {
		currentReplicas := *existingSts.Spec.Replicas
		if desiredReplicas < currentReplicas {
			r.gracefulScaleDown(ctx, cr, int(currentReplicas), int(desiredReplicas))
		}
	}

	if err := r.createOrUpdate(ctx, cr, sts); err != nil {
		return fmt.Errorf("statefulset: %w", err)
	}

	// 5. ServiceMonitor (optional — only when monitoring is enabled and CRD exists)
	if cr.Spec.Monitoring.ServiceMonitor.Enabled {
		sm := buildServiceMonitor(cr)
		if sm != nil {
			if err := r.createOrUpdate(ctx, cr, sm); err != nil {
				// Log but don't fail the entire reconciliation for a monitoring issue
				logger.Error(err, "failed to reconcile servicemonitor")
			}
		}
	}

	managedClusters.Set(1) // This instance exists and is managed
	logger.Info("resources reconciled", "name", cr.Name, "replicas", desiredReplicas)
	return nil
}

// gracefulScaleDown calls /cluster/leave for each node being removed.
// Errors are logged but not returned — we proceed with scale-down even if the API call fails.
func (r *CacheClusterReconciler) gracefulScaleDown(ctx context.Context, cr *cachev1.CacheCluster, current, desired int) {
	logger := log.FromContext(ctx)
	svc := headlessServiceName(cr)
	ns := cr.Namespace

	// List pods to find the leader
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList, client.InNamespace(ns), client.MatchingLabels(labels(cr))); err != nil {
		logger.Info("unable to list pods for graceful scale-down, proceeding", "error", err)
		return
	}

	leaderIP := leaderPodIP(ctx, r.Client, ns, podList.Items, cr.Spec.TLS.Enabled)
	if leaderIP == "" {
		logger.Info("no leader found for graceful scale-down, proceeding directly")
		return
	}

	for i := desired; i < current; i++ {
		podName := fmt.Sprintf("%s-%d", statefulSetName(cr), i)
		raftAddr := raftAddrForPod(podName, svc, ns)
		logger.Info("gracefully removing node from Raft cluster", "pod", podName, "raftAddr", raftAddr)
		if err := removeNodeFromRaft(ctx, leaderIP, raftAddr, cr.Spec.TLS.Enabled); err != nil {
			logger.Info("failed to remove node from Raft, will still proceed with scale-down",
				"pod", podName, "error", err)
		}
	}
}

func (r *CacheClusterReconciler) createOrUpdate(ctx context.Context, cr *cachev1.CacheCluster, obj client.Object) error {
	logger := log.FromContext(ctx)
	kind := obj.GetObjectKind().GroupVersionKind().Kind

	// Set owner reference
	if err := controllerutil.SetControllerReference(cr, obj, r.Scheme); err != nil {
		return err
	}

	existing := obj.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		logger.Info("creating resource", "kind", kind, "name", obj.GetName())
		return r.Create(ctx, obj)
	}

	// Update existing - preserve labels and annotations from the desired state
	existing.SetLabels(obj.GetLabels())
	if existing.GetAnnotations() == nil {
		existing.SetAnnotations(obj.GetAnnotations())
	} else {
		for k, v := range obj.GetAnnotations() {
			existing.GetAnnotations()[k] = v
		}
	}

	// Set the spec directly based on the type.
	// Note: ClusterIP and VolumeClaimTemplates are intentionally NOT copied
	// because they are immutable on existing objects in Kubernetes.
	applySpec(existing, obj)

	logger.Info("updating resource", "kind", kind, "name", obj.GetName())
	// Retry once on conflict (the object may have been modified between GET and UPDATE).
	if err := r.Update(ctx, existing); err != nil {
		if apierrors.IsConflict(err) {
			// Refetch and retry
			latest := obj.DeepCopyObject().(client.Object)
			if err2 := r.Get(ctx, client.ObjectKeyFromObject(obj), latest); err2 != nil {
				return err2
			}
			// Re-apply spec and labels
			latest.SetLabels(obj.GetLabels())
			applySpec(latest, obj)
			return r.Update(ctx, latest)
		}
		return err
	}
	return nil
}

// applySpec copies type-specific spec fields from desired to existing.
func applySpec(existing, desired client.Object) {
	switch d := desired.(type) {
	case *corev1.Service:
		svc := existing.(*corev1.Service)
		svc.Spec.Ports = d.Spec.Ports
		svc.Spec.Selector = d.Spec.Selector
		svc.Spec.Type = d.Spec.Type
		svc.Spec.PublishNotReadyAddresses = d.Spec.PublishNotReadyAddresses
	case *corev1.ConfigMap:
		cm := existing.(*corev1.ConfigMap)
		cm.Data = d.Data
	case *appsv1.StatefulSet:
		e := existing.(*appsv1.StatefulSet)
		e.Spec.Replicas = d.Spec.Replicas
		e.Spec.Template = d.Spec.Template
		e.Spec.UpdateStrategy = d.Spec.UpdateStrategy
	case *unstructured.Unstructured:
		desiredSpec, _, _ := unstructured.NestedMap(d.Object, "spec")
		if desiredSpec != nil {
			unstructured.SetNestedField(existing.(*unstructured.Unstructured).Object, desiredSpec, "spec")
		}
	}
}

func (r *CacheClusterReconciler) updateStatus(ctx context.Context, cr *cachev1.CacheCluster) error {
	status, err := collectStatus(ctx, cr, r.Client)
	if err != nil {
		return fmt.Errorf("collect status: %w", err)
	}

	// Retry on conflict
	var lastErr error
	nn := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
	for i := 0; i < statusRetryAttempts; i++ {
		latest := &cachev1.CacheCluster{}
		if err := r.Get(ctx, nn, latest); err != nil {
			return err
		}
		latest.Status = *status
		if err := r.Status().Update(ctx, latest); err != nil {
			lastErr = err
			if apierrors.IsConflict(err) {
				// Exponential backoff: 100ms, 200ms, 400ms
				time.Sleep(statusRetryDelay * (1 << i))
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("status update retries exhausted: %w", lastErr)
}

func (r *CacheClusterReconciler) reconcileDelete(ctx context.Context, cr *cachev1.CacheCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(cr, finalizerName) {
		logger.Info("removing finalizer", "name", cr.Name)
		controllerutil.RemoveFinalizer(cr, finalizerName)
		if err := r.Update(ctx, cr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Clear metrics for this cluster
	managedNodes.Set(0)
	managedClusters.Set(0)

	return ctrl.Result{}, nil
}

// mapPodToCluster maps Pod events back to the owning CacheCluster.
func (r *CacheClusterReconciler) mapPodToCluster(ctx context.Context, obj client.Object) []ctrl.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	// Find owning CacheCluster by matching labels
	instanceName := pod.Labels[labelAppInstance]
	if instanceName == "" {
		return nil
	}

	return []ctrl.Request{{
		NamespacedName: types.NamespacedName{
			Name:      instanceName,
			Namespace: pod.Namespace,
		},
	}}
}

// podStatusChangePredicate filters Pod events to only those where the status changed.
func podStatusChangePredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldPod, ok1 := e.ObjectOld.(*corev1.Pod)
			newPod, ok2 := e.ObjectNew.(*corev1.Pod)
			if !ok1 || !ok2 {
				return false
			}
			// Only reconcile if pod IP, ready status, or phase changed
			return oldPod.Status.PodIP != newPod.Status.PodIP ||
				oldPod.Status.Phase != newPod.Status.Phase ||
				podReadyCondition(oldPod) != podReadyCondition(newPod)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true // Reconcile on Pod deletion
		},
	}
}

func podReadyCondition(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (r *CacheClusterReconciler) validateSpec(ctx context.Context, cr *cachev1.CacheCluster) error {
	replicas := cr.Spec.Replicas
	if replicas < 1 {
		return fmt.Errorf("replicas must be >= 1, got %d", replicas)
	}
	if replicas > 1 && replicas%2 == 0 {
		return fmt.Errorf("replicas must be odd for Raft consensus (got %d)", replicas)
	}

	// Validate referenced Secrets exist
	if cr.Spec.Auth.Enabled && cr.Spec.Auth.TokenSecretRef.Name != "" {
		if err := r.checkSecret(ctx, cr.Namespace, cr.Spec.Auth.TokenSecretRef.Name); err != nil {
			return fmt.Errorf("auth tokenSecretRef: %w", err)
		}
	}
	if cr.Spec.TLS.Enabled && cr.Spec.TLS.CertSecretRef.Name != "" {
		if err := r.checkSecret(ctx, cr.Namespace, cr.Spec.TLS.CertSecretRef.Name); err != nil {
			return fmt.Errorf("tls certSecretRef: %w", err)
		}
	}

	return nil
}

func (r *CacheClusterReconciler) checkSecret(ctx context.Context, namespace, name string) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	if err := r.Get(ctx, key, &corev1.Secret{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("Secret %q not found in namespace %q", name, namespace)
		}
		return err
	}
	return nil
}
