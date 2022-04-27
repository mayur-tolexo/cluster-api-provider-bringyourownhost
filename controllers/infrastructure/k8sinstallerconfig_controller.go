// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// K8sInstallerConfigReconciler reconciles a K8sInstallerConfig object
type K8sInstallerConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=k8sinstallerconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=k8sinstallerconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=k8sinstallerconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the K8sInstallerConfig object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.0/pkg/reconcile
func (r *K8sInstallerConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received")

	// Fetch the K8sInstallerConfig instance
	config := &infrav1.K8sInstallerConfig{}
	err := r.Client.Get(ctx, req.NamespacedName, config)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get K8sInstallerConfig")
		return ctrl.Result{}, err
	}

	// Fetch the ByoMachine
	byomachine, err := GetOwnerByoMachine(ctx, r.Client, config.ObjectMeta)
	if err != nil {
		logger.Error(err, "failed to get Owner ByoMachine")
		return ctrl.Result{}, err
	}

	if byomachine == nil {
		logger.Info("Waiting for ByoMachine Controller to set OwnerRef on InstallerConfig")
		return ctrl.Result{}, nil
	}
	logger.Info("got byo machine", byomachine.Name)
	// if byomachine.Status.HostInfo == nil {

	// }

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *K8sInstallerConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.K8sInstallerConfig{}).
		Watches(
			&source.Kind{Type: &infrav1.ByoMachine{}},
			handler.EnqueueRequestsFromMapFunc(r.ByoMachineToK8sInstallerConfigMapFunc),
		).
		Complete(r)
}

// ByoMachineToK8sInstallerConfigMapFunc is a handler.ToRequestsFunc to be used to enqeue
// request for reconciliation of K8sInstallerConfig.
func (r *K8sInstallerConfigReconciler) ByoMachineToK8sInstallerConfigMapFunc(o client.Object) []ctrl.Request {
	m, ok := o.(*infrav1.ByoMachine)
	if !ok {
		panic(fmt.Sprintf("Expected a ByoMachine but got a %T", o))
	}

	result := []ctrl.Request{}
	if m.Spec.InstallerRef != nil && m.Spec.InstallerRef.GroupVersionKind() == infrav1.GroupVersion.WithKind("K8sInstallerConfig") {
		name := client.ObjectKey{Namespace: m.Spec.InstallerRef.Namespace, Name: m.Spec.InstallerRef.Name}
		result = append(result, ctrl.Request{NamespacedName: name})
	}
	return result
}

// GetOwnerByoMachine returns the ByoMachine object owning the current resource.
func GetOwnerByoMachine(ctx context.Context, c client.Client, obj metav1.ObjectMeta) (*infrav1.ByoMachine, error) {
	for _, ref := range obj.OwnerReferences {
		gv, err := schema.ParseGroupVersion(ref.APIVersion)
		if err != nil {
			return nil, err
		}
		if ref.Kind == "ByoMachine" && gv.Group == infrav1.GroupVersion.Group {
			return GetByoMachineByName(ctx, c, obj.Namespace, ref.Name)
		}
	}
	return nil, nil
}

// GetByoMachineByName finds and return a ByoMachine object using the specified params.
func GetByoMachineByName(ctx context.Context, c client.Client, namespace, name string) (*infrav1.ByoMachine, error) {
	m := &infrav1.ByoMachine{}
	key := client.ObjectKey{Name: name, Namespace: namespace}
	if err := c.Get(ctx, key, m); err != nil {
		return nil, err
	}
	return m, nil
}
