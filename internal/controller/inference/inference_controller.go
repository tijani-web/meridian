/*
Package inference implements the Kubernetes controller for Meridian InferenceDeployment CRDs (§6.6).
*/
package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

// Reconciler reconciles an InferenceDeployment object.
type Reconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	MetricsProvider MetricsProvider
}

// +kubebuilder:rbac:groups=meridian.dev,resources=inferencedeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=meridian.dev,resources=inferencedeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;pods,verbs=get;list;watch;create;update;patch;delete

// Reconcile coordinates revisions, warm-up gating, canary promotion, and routing publication.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	inf := &meridianv1alpha1.InferenceDeployment{}
	if err := r.Get(ctx, req.NamespacedName, inf); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get InferenceDeployment: %w", err)
	}

	// 1. Deletion & Finalizer handling
	if !inf.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(inf, FinalizerName) {
			logger.Info("Cleaning up InferenceDeployment resources", "name", inf.Name)
			controllerutil.RemoveFinalizer(inf, FinalizerName)
			if err := r.Update(ctx, inf); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(inf, FinalizerName) {
		controllerutil.AddFinalizer(inf, FinalizerName)
		if err := r.Update(ctx, inf); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 2. Compute Revision Name for current spec
	revHash := computeRevisionHash(inf)
	currentRevName := fmt.Sprintf("%s-%s", inf.Name, revHash[:8])

	// 3. Reconcile Revisions (Canary vs Single)
	revisions, err := r.reconcileRevisions(ctx, inf, currentRevName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile revisions: %w", err)
	}

	// 4. Publish Routing Table ConfigMap
	if err := r.publishRoutingTable(ctx, inf, revisions); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to publish routing table: %w", err)
	}

	// 5. Update Status
	if err := r.updateStatus(ctx, inf, revisions); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
	}

	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// reconcileRevisions manages child Deployments and Services for active model revisions.
func (r *Reconciler) reconcileRevisions(
	ctx context.Context,
	inf *meridianv1alpha1.InferenceDeployment,
	targetRevName string,
) ([]meridianv1alpha1.RevisionStatus, error) {
	// Discover existing revisions from status
	var activeRevs []meridianv1alpha1.RevisionStatus
	var stableRev *meridianv1alpha1.RevisionStatus

	for _, rev := range inf.Status.Revisions {
		if rev.Weight > 0 {
			activeRevs = append(activeRevs, rev)
			if rev.Name != targetRevName && stableRev == nil {
				rCopy := rev
				stableRev = &rCopy
			}
		}
	}

	// Case 1: Initial deployment or single revision
	if len(activeRevs) == 0 || (len(activeRevs) == 1 && activeRevs[0].Name == targetRevName) {
		revStatus, err := r.ensureRevisionDeployment(ctx, inf, targetRevName, 100, inf.Spec.Replicas)
		if err != nil {
			return nil, err
		}
		return []meridianv1alpha1.RevisionStatus{revStatus}, nil
	}

	// Case 2: Rollout in progress (Canary or BlueGreen)
	if inf.Spec.Rollout.Strategy == meridianv1alpha1.RolloutStrategyCanary && len(inf.Spec.Rollout.Steps) > 0 {
		return r.progressCanaryRollout(ctx, inf, stableRev, targetRevName)
	}

	// Default fallback: direct update to target revision
	revStatus, err := r.ensureRevisionDeployment(ctx, inf, targetRevName, 100, inf.Spec.Replicas)
	if err != nil {
		return nil, err
	}
	return []meridianv1alpha1.RevisionStatus{revStatus}, nil
}

// progressCanaryRollout executes the stepwise canary promotion or automated rollback.
func (r *Reconciler) progressCanaryRollout(
	ctx context.Context,
	inf *meridianv1alpha1.InferenceDeployment,
	stableRev *meridianv1alpha1.RevisionStatus,
	canaryRevName string,
) ([]meridianv1alpha1.RevisionStatus, error) {
	logger := log.FromContext(ctx)

	// Step index tracking
	stepIdx := 0
	if val, ok := inf.Annotations[AnnotationCanaryStep]; ok {
		if parsed, err := strconv.Atoi(val); err == nil && parsed >= 0 {
			stepIdx = parsed
		}
	}

	steps := inf.Spec.Rollout.Steps
	if stepIdx >= len(steps) {
		stepIdx = len(steps) - 1
	}
	activeStep := steps[stepIdx]

	// 1. Ensure Canary Deployment exists with at least 1 replica for testing
	canaryReplicas := int32(1)
	if inf.Spec.Replicas > 1 {
		canaryReplicas = (inf.Spec.Replicas * activeStep.Weight) / 100
		if canaryReplicas < 1 {
			canaryReplicas = 1
		}
	}

	canaryStatus, err := r.ensureRevisionDeployment(ctx, inf, canaryRevName, activeStep.Weight, canaryReplicas)
	if err != nil {
		return nil, err
	}

	// Ensure Stable Deployment remains running
	stableWeight := 100 - activeStep.Weight
	stableName := inf.Name + "-stable"
	if stableRev != nil {
		stableName = stableRev.Name
	}
	stableStatus, err := r.ensureRevisionDeployment(ctx, inf, stableName, stableWeight, inf.Spec.Replicas)
	if err != nil {
		return nil, err
	}

	// 2. Warm-Up Gating: If canary is not warm yet, hold traffic and wait
	if !canaryStatus.Warm {
		logger.Info("Canary revision warming up; gating traffic", "canary", canaryRevName)
		canaryStatus.Weight = 0
		stableStatus.Weight = 100
		return []meridianv1alpha1.RevisionStatus{stableStatus, canaryStatus}, nil
	}

	// 3. Automated Metric Analysis
	metrics := r.MetricsProvider
	if metrics == nil {
		metrics = &StaticMetricsProvider{}
	}

	analysisRes := EvaluateRolloutStep(ctx, metrics, inf.Namespace, canaryRevName, activeStep.Analysis)

	// 4. Automated Rollback Triggered
	if analysisRes.Decision == DecisionRollback {
		logger.Error(fmt.Errorf("%s", analysisRes.Reason), "Canary failed metric criteria; executing AUTOMATED ROLLBACK",
			"canary", canaryRevName)

		// Rollback: route 100% traffic back to stable, set canary weight to 0
		canaryStatus.Weight = 0
		stableStatus.Weight = 100

		if inf.Annotations == nil {
			inf.Annotations = make(map[string]string)
		}
		inf.Annotations[AnnotationRollbackReason] = analysisRes.Reason
		_ = r.Update(ctx, inf)

		// Set condition
		metaCondition := metav1.Condition{
			Type:               "RolloutAborted",
			Status:             metav1.ConditionTrue,
			Reason:             "AutomatedRollback",
			Message:            analysisRes.Reason,
			LastTransitionTime: metav1.Now(),
		}
		inf.Status.Conditions = append(inf.Status.Conditions, metaCondition)

		return []meridianv1alpha1.RevisionStatus{stableStatus, canaryStatus}, nil
	}

	// 5. Automated Promotion Step
	if analysisRes.Decision == DecisionPromote {
		if activeStep.Weight >= 100 || stepIdx == len(steps)-1 {
			// Final promotion: canary fully replaces stable
			logger.Info("Canary rollout complete; fully promoted", "revision", canaryRevName)
			canaryStatus.Weight = 100
			canaryStatus.ReadyReplicas = inf.Spec.Replicas
			// Scale down old stable
			_, _ = r.ensureRevisionDeployment(ctx, inf, stableName, 0, 0)
			return []meridianv1alpha1.RevisionStatus{canaryStatus}, nil
		}

		// Advance to next step
		if inf.Annotations == nil {
			inf.Annotations = make(map[string]string)
		}
		inf.Annotations[AnnotationCanaryStep] = strconv.Itoa(stepIdx + 1)
		_ = r.Update(ctx, inf)
	}

	return []meridianv1alpha1.RevisionStatus{stableStatus, canaryStatus}, nil
}

// ensureRevisionDeployment creates or updates the child Deployment and Service for a revision.
func (r *Reconciler) ensureRevisionDeployment(
	ctx context.Context,
	inf *meridianv1alpha1.InferenceDeployment,
	revName string,
	weight int32,
	replicas int32,
) (meridianv1alpha1.RevisionStatus, error) {
	labels := map[string]string{
		LabelInferenceDeployment: inf.Name,
		LabelRevision:            revName,
	}

	gpuQty := resource.MustParse(fmt.Sprintf("%d", inf.Spec.Resources.GPUsPerReplica))

	// Pod Spec with Meridian scheduler and GPU requests
	podSpec := corev1.PodSpec{
		SchedulerName: "meridian-scheduler",
		Containers: []corev1.Container{
			{
				Name:  "vllm-engine",
				Image: inf.Spec.Runtime.Image,
				Args:  inf.Spec.Runtime.Args,
				Ports: []corev1.ContainerPort{
					{
						Name:          "http",
						ContainerPort: inf.Spec.Runtime.Port,
					},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): gpuQty,
					},
					Requests: corev1.ResourceList{
						corev1.ResourceName("nvidia.com/gpu"): gpuQty,
					},
				},
			},
		},
	}

	if inf.Spec.Warmup.Probe != nil {
		podSpec.Containers[0].ReadinessProbe = inf.Spec.Warmup.Probe
	}

	desiredDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      revName,
			Namespace: inf.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: podSpec,
			},
		},
	}
	if err := controllerutil.SetControllerReference(inf, desiredDeploy, r.Scheme); err != nil {
		return meridianv1alpha1.RevisionStatus{}, err
	}

	// Upsert Deployment
	existingDeploy := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desiredDeploy), existingDeploy)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desiredDeploy); err != nil {
			return meridianv1alpha1.RevisionStatus{}, fmt.Errorf("create deployment %s: %w", revName, err)
		}
		existingDeploy = desiredDeploy
	} else if err == nil {
		existingDeploy.Spec.Replicas = &replicas
		existingDeploy.Spec.Template = desiredDeploy.Spec.Template
		if err := r.Update(ctx, existingDeploy); err != nil {
			return meridianv1alpha1.RevisionStatus{}, fmt.Errorf("update deployment %s: %w", revName, err)
		}
	} else {
		return meridianv1alpha1.RevisionStatus{}, err
	}

	// Ensure Service
	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      revName,
			Namespace: inf.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       inf.Spec.Runtime.Port,
					TargetPort: intstr.FromInt(int(inf.Spec.Runtime.Port)),
				},
			},
		},
	}
	_ = controllerutil.SetControllerReference(inf, desiredSvc, r.Scheme)

	existingSvc := &corev1.Service{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(desiredSvc), existingSvc); apierrors.IsNotFound(err) {
		_ = r.Create(ctx, desiredSvc)
	}

	readyReplicas := existingDeploy.Status.ReadyReplicas
	// Warm-up check: replica must be Ready
	isWarm := readyReplicas > 0

	return meridianv1alpha1.RevisionStatus{
		Name:          revName,
		Weight:        weight,
		ReadyReplicas: readyReplicas,
		Warm:          isWarm,
	}, nil
}

// publishRoutingTable exports the published routes ConfigMap for the inference router data plane.
func (r *Reconciler) publishRoutingTable(
	ctx context.Context,
	inf *meridianv1alpha1.InferenceDeployment,
	revisions []meridianv1alpha1.RevisionStatus,
) error {
	var pubRevs []PublishedRevision
	for _, rev := range revisions {
		endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
			rev.Name, inf.Namespace, inf.Spec.Runtime.Port)
		pubRevs = append(pubRevs, PublishedRevision{
			Name:      rev.Name,
			Weight:    rev.Weight,
			Warm:      rev.Warm,
			Endpoints: []string{endpoint},
		})
	}

	table := PublishedRoutingTable{
		Deployment:    inf.Name,
		Namespace:     inf.Namespace,
		Strategy:      inf.Spec.Routing.Strategy,
		MaxQueueDepth: 100,
		Revisions:     pubRevs,
	}
	if inf.Spec.Routing.LoadShedding != nil && inf.Spec.Routing.LoadShedding.MaxQueueDepth > 0 {
		table.MaxQueueDepth = inf.Spec.Routing.LoadShedding.MaxQueueDepth
	}

	dataBytes, err := json.MarshalIndent(table, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal routing table: %w", err)
	}

	cmName := inf.Name + RoutingConfigMapSuffix
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: inf.Namespace,
			Labels: map[string]string{
				LabelInferenceDeployment: inf.Name,
				"meridian.dev/component": "inference-routing-table",
			},
		},
		Data: map[string]string{
			"routes.json": string(dataBytes),
		},
	}
	_ = controllerutil.SetControllerReference(inf, cm, r.Scheme)

	existingCM := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(cm), existingCM); apierrors.IsNotFound(err) {
		return r.Create(ctx, cm)
	} else if err == nil {
		existingCM.Data = cm.Data
		return r.Update(ctx, existingCM)
	}
	return err
}

func (r *Reconciler) updateStatus(
	ctx context.Context,
	inf *meridianv1alpha1.InferenceDeployment,
	revisions []meridianv1alpha1.RevisionStatus,
) error {
	inf.Status.Revisions = revisions
	inf.Status.ObservedGeneration = inf.Generation

	// Set Available condition
	totalReady := int32(0)
	for _, rev := range revisions {
		totalReady += rev.ReadyReplicas
	}

	condStatus := metav1.ConditionFalse
	reason := "ReplicasNotReady"
	msg := "Waiting for replicas to become ready and warm"
	if totalReady > 0 {
		condStatus = metav1.ConditionTrue
		reason = "ReplicasAvailable"
		msg = fmt.Sprintf("%d replicas ready and serving", totalReady)
	}

	availCond := metav1.Condition{
		Type:               "Available",
		Status:             condStatus,
		Reason:             reason,
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	}

	found := false
	for i, c := range inf.Status.Conditions {
		if c.Type == "Available" {
			inf.Status.Conditions[i] = availCond
			found = true
			break
		}
	}
	if !found {
		inf.Status.Conditions = append(inf.Status.Conditions, availCond)
	}

	return r.Status().Update(ctx, inf)
}

func computeRevisionHash(inf *meridianv1alpha1.InferenceDeployment) string {
	hasher := sha256.New()
	hasher.Write([]byte(inf.Spec.Model.Name))
	hasher.Write([]byte(inf.Spec.Model.URI))
	hasher.Write([]byte(inf.Spec.Model.Revision))
	hasher.Write([]byte(inf.Spec.Runtime.Image))
	hasher.Write([]byte(fmt.Sprintf("%d", inf.Spec.Runtime.Port)))
	hasher.Write([]byte(fmt.Sprintf("%d", inf.Spec.Resources.GPUsPerReplica)))
	return hex.EncodeToString(hasher.Sum(nil))
}

// SetupWithManager registers the InferenceDeployment controller with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&meridianv1alpha1.InferenceDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
