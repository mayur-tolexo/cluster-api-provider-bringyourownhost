// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	"github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/agent/installer"
	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	infrav1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/pointer"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
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
func (r *K8sInstallerConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
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
	byoMachine, err := GetOwnerByoMachine(ctx, r.Client, config.ObjectMeta)
	if err != nil {
		logger.Error(err, "failed to get Owner ByoMachine")
		return ctrl.Result{}, err
	}

	if byoMachine == nil {
		logger.Info("Waiting for ByoMachine Controller to set OwnerRef on InstallerConfig")
		return ctrl.Result{}, nil
	}

	// Fetch the Cluster
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		logger.Error(err, "ByoMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}

	if cluster == nil {
		logger.Info(fmt.Sprintf("Please associate this machine with a cluster using the label %s: <name of cluster>", clusterv1.ClusterLabelName))
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("cluster", cluster.Name)

	if annotations.IsPaused(cluster, config) {
		logger.Info("Reconciliation is paused for this object")
		return ctrl.Result{}, nil
	}

	helper, err := patch.NewHelper(config, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err = helper.Patch(ctx, config); err != nil && reterr == nil {
			logger.Error(err, "failed to patch K8sInstallerConfig")
			reterr = err
		}
	}()

	// Create the K8sInstallerConfig scope
	scope := &k8sInstallerConfigScope{
		Client:      r.Client,
		PatchHelper: helper,
		Cluster:     cluster,
		ByoMachine:  byoMachine,
		Config:      config,
	}

	// Handle deleted K8sInstallerConfig
	if !config.ObjectMeta.DeletionTimestamp.IsZero() {
		// return r.reconcileDelete(ctx, machineScope)
	}

	return r.reconcileNormal(ctx, scope, logger)
}

func (r *K8sInstallerConfigReconciler) reconcileNormal(ctx context.Context, scope *k8sInstallerConfigScope, logger logr.Logger) (reconcile.Result, error) {
	installer, err := NewInstaller(ctx, scope.ByoMachine.Status.HostInfo.OSImage, scope.Config.Spec.BundleType)
	if err != nil {
		logger.Error(err, "failed to create K8sInstaller instance")
		return ctrl.Result{}, err
	}
	if err := r.storeInstallationData(ctx, scope, installer.Install(), installer.Uninstall()); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// storeInstallationData creates a new secret with the installation and uninstallation data passed in as input,
// sets the reference in the configuration status and ready to true.
func (r *K8sInstallerConfigReconciler) storeInstallationData(ctx context.Context, scope *k8sInstallerConfigScope, install, uninstall string) error {
	logger := ctrl.LoggerFrom(ctx)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scope.Config.Name,
			Namespace: scope.Config.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterLabelName: scope.Cluster.Name,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: infrav1.GroupVersion.String(),
					Kind:       "K8sInstallerConfig",
					Name:       scope.Config.Name,
					UID:        scope.Config.UID,
					Controller: pointer.BoolPtr(true),
				},
			},
		},
		Data: map[string][]byte{
			"install":   []byte(install),
			"uninstall": []byte(uninstall),
		},
		Type: clusterv1.ClusterSecretType,
	}

	// as secret creation and scope.Config status patch are not atomic operations
	// it is possible that secret creation happens but the config.Status patches are not applied
	if err := r.Client.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return errors.Wrapf(err, "failed to create install data secret for K8sInstallerConfig %s/%s", scope.Config.Namespace, scope.Config.Name)
		}
		logger.Info("install data secret for K8sInstallerConfig already exists, updating", "secret", secret.Name, "K8sInstallerConfig", scope.Config.Name)
		if err := r.Client.Update(ctx, secret); err != nil {
			return errors.Wrapf(err, "failed to update install data secret for K8sInstallerConfig %s/%s", scope.Config.Namespace, scope.Config.Name)
		}
	}
	scope.Config.Status.InstallationSecret = &corev1.ObjectReference{
		Kind:       secret.Kind,
		Namespace:  secret.Namespace,
		Name:       secret.Name,
		UID:        secret.UID,
		APIVersion: secret.APIVersion,
	}
	scope.Config.Status.Ready = true
	return nil
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

// K8sInstaller represent k8s installer interface
type K8sInstaller interface {
	Install() string
	Uninstall() string
}

// NewInstaller will return a new installer
func NewInstaller(ctx context.Context, osDist, k8sVersion string) (K8sInstaller, error) {
	reg := installer.GetSupportedRegistry(nil)
	if len(reg.ListK8s(osDist)) == 0 {
		// return nil, installer.ErrOsK8sNotSupported
	}
	return &Ubuntu20_4Installer{
		install:   installer.DoUbuntu20_4K8s1_22,
		uninstall: installer.UndoUbuntu20_4K8s1_22,
	}, nil
}

// Ubuntu20_4Installer represent the installer implementation for ubunto20.4.* os distribution
type Ubuntu20_4Installer struct {
	install   string
	uninstall string
}

// Install will return k8s install script
func (s *Ubuntu20_4Installer) Install() string {
	return s.install
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu20_4Installer) Uninstall() string {
	return s.uninstall
}
