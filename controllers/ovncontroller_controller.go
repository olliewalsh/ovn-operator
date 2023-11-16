/*
Copyright 2022.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	certmgrv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/go-logr/logr"
	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openstack-k8s-operators/lib-common/modules/certmanager"
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	"github.com/openstack-k8s-operators/lib-common/modules/common/daemonset"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	"github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	"github.com/openstack-k8s-operators/lib-common/modules/common/job"
	"github.com/openstack-k8s-operators/lib-common/modules/common/labels"
	nad "github.com/openstack-k8s-operators/lib-common/modules/common/networkattachment"
	common_rbac "github.com/openstack-k8s-operators/lib-common/modules/common/rbac"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/lib-common/modules/common/util"
	"github.com/openstack-k8s-operators/ovn-operator/api/v1beta1"
	"github.com/openstack-k8s-operators/ovn-operator/pkg/ovncontroller"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// getlog returns a logger object with a prefix of "conroller.name" and aditional controller context fields
func (r *OVNControllerReconciler) GetLogger(ctx context.Context) logr.Logger {
	return log.FromContext(ctx).WithName("Controllers").WithName("OVNController")
}

// OVNControllerReconciler reconciles a OVNController object
type OVNControllerReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Scheme  *runtime.Scheme
}

// GetClient -
func (r *OVNControllerReconciler) GetClient() client.Client {
	return r.Client
}

//+kubebuilder:rbac:groups=ovn.openstack.org,resources=ovncontrollers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ovn.openstack.org,resources=ovncontrollers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ovn.openstack.org,resources=ovncontrollers/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete;
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;
//+kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=create;delete;get;list;patch;update;watch
//+kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch;update;delete;
//+kubebuilder:rbac:groups=ovn.openstack.org,resources=ovndbclusters,verbs=get;list;watch;
//+kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=create;delete;get;list;patch;update;watch
//+kubebuilder:rbac:groups=cert-manager.io,resources=issuers,verbs=get;list;watch
//+kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete;

// service account, role, rolebinding
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=roles,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="rbac.authorization.k8s.io",resources=rolebindings,verbs=get;list;watch;create;update
// service account permissions that are needed to grant permission to the above
// +kubebuilder:rbac:groups="security.openshift.io",resourceNames=anyuid;privileged,resources=securitycontextconstraints,verbs=use
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;patch;update;watch

func (r *OVNControllerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, _err error) {

	Log := r.GetLogger(ctx)

	// Fetch OVNController instance
	instance := &v1beta1.OVNController{}
	err := r.Client.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if k8s_errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// For additional cleanup logic use finalizers. Return and don't requeue.
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	helper, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		// update the Ready condition based on the sub conditions
		if instance.Status.Conditions.AllSubConditionIsTrue() {
			instance.Status.Conditions.MarkTrue(
				condition.ReadyCondition, condition.ReadyMessage)
		} else {
			// something is not ready so reset the Ready condition
			instance.Status.Conditions.MarkUnknown(
				condition.ReadyCondition, condition.InitReason, condition.ReadyInitMessage)
			// and recalculate it based on the state of the rest of the conditions
			instance.Status.Conditions.Set(
				instance.Status.Conditions.Mirror(condition.ReadyCondition))
		}
		err := helper.PatchInstance(ctx, instance)
		if err != nil {
			_err = err
			return
		}
	}()

	// If we're not deleting this and the service object doesn't have our finalizer, add it.
	if instance.DeletionTimestamp.IsZero() && controllerutil.AddFinalizer(instance, helper.GetFinalizer()) {
		return ctrl.Result{}, nil
	}

	//
	// initialize status
	//
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
		// initialize conditions used later as Status=Unknown
		cl := condition.CreateList(
			condition.UnknownCondition(condition.InputReadyCondition, condition.InitReason, condition.InputReadyInitMessage),
			condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
			condition.UnknownCondition(condition.NetworkAttachmentsReadyCondition, condition.InitReason, condition.NetworkAttachmentsReadyInitMessage),
			condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
			condition.UnknownCondition(condition.ServiceAccountReadyCondition, condition.InitReason, condition.ServiceAccountReadyInitMessage),
			condition.UnknownCondition(condition.RoleReadyCondition, condition.InitReason, condition.RoleReadyInitMessage),
			condition.UnknownCondition(condition.RoleBindingReadyCondition, condition.InitReason, condition.RoleBindingReadyInitMessage),
		)

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		return ctrl.Result{}, nil
	}

	if instance.Status.Hash == nil {
		instance.Status.Hash = map[string]string{}
	}
	if instance.Status.NetworkAttachments == nil {
		instance.Status.NetworkAttachments = map[string][]string{}
	}

	// Handle service delete
	if !instance.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, instance, helper)
	}

	// Handle non-deleted clusters
	return r.reconcileNormal(ctx, instance, helper)
}

// SetupWithManager sets up the controller with the Manager.
func (r *OVNControllerReconciler) SetupWithManager(mgr ctrl.Manager, ctx context.Context) error {
	crs := &v1beta1.OVNControllerList{}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.OVNController{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Owns(&netattdefv1.NetworkAttachmentDefinition{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&source.Kind{Type: &v1beta1.OVNDBCluster{}}, handler.EnqueueRequestsFromMapFunc(v1beta1.OVNDBClusterNamespaceMapFunc(crs, mgr.GetClient(), r.GetLogger(ctx)))).
		Complete(r)
}

func (r *OVNControllerReconciler) reconcileDelete(ctx context.Context, instance *v1beta1.OVNController, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service delete")

	// Service is deleted so remove the finalizer.
	controllerutil.RemoveFinalizer(instance, helper.GetFinalizer())
	Log.Info("Reconciled Service delete successfully")

	return ctrl.Result{}, nil
}

func (r *OVNControllerReconciler) reconcileInit(
	ctx context.Context,
	instance *v1beta1.OVNController,
	helper *helper.Helper,
) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service init")

	//TODO(slaweq):
	// * read status of the external IDs
	// * if external IDs are different than required once, change them
	Log.Info("Reconciled Service init successfully")
	return ctrl.Result{}, nil
}

func (r *OVNControllerReconciler) reconcileUpdate(ctx context.Context, instance *v1beta1.OVNController, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service update")

	Log.Info("Reconciled Service update successfully")
	return ctrl.Result{}, nil
}

func (r *OVNControllerReconciler) reconcileUpgrade(ctx context.Context, instance *v1beta1.OVNController, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service upgrade")

	Log.Info("Reconciled Service upgrade successfully")
	return ctrl.Result{}, nil
}

func (r *OVNControllerReconciler) reconcileNormal(ctx context.Context, instance *v1beta1.OVNController, helper *helper.Helper) (ctrl.Result, error) {
	Log := r.GetLogger(ctx)

	Log.Info("Reconciling Service")

	// Service account, role, binding
	rbacRules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{"security.openshift.io"},
			ResourceNames: []string{"anyuid", "privileged"},
			Resources:     []string{"securitycontextconstraints"},
			Verbs:         []string{"use"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"pods"},
			Verbs:     []string{"create", "get", "list", "watch", "update", "patch", "delete"},
		},
	}
	rbacResult, err := common_rbac.ReconcileRbac(ctx, helper, instance, rbacRules)
	if err != nil {
		return rbacResult, err
	} else if (rbacResult != ctrl.Result{}) {
		return rbacResult, nil
	}

	// ConfigMap
	configMapVars := make(map[string]env.Setter)

	instance.Status.Conditions.MarkTrue(condition.InputReadyCondition, condition.InputReadyMessage)

	// TODO(owalsh): handle OVN-controller TLS cert/key

	//
	// create Configmap required for OVNController input
	// - %-scripts configmap holding scripts to e.g. bootstrap the service
	//
	err = r.generateServiceConfigMaps(ctx, helper, instance, &configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	}

	//
	// create hash over all the different input resources to identify if any those changed
	// and a restart/recreate is required.
	//
	inputHash, hashChanged, err := r.createHashOfInputHashes(ctx, instance, configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, err
	} else if hashChanged {
		// Hash changed and instance status should be updated (which will be done by main defer func),
		// so we need to return and reconcile again
		return ctrl.Result{}, nil
	}
	// TODO(slaweq): configure service (ovn controller and ovs settings)
	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)

	//
	// TODO check when/if Init, Update, or Upgrade should/could be skipped
	//

	serviceLabels := map[string]string{
		common.AppSelector: ovncontroller.ServiceName,
	}

	// Create additional Physical Network Attachments
	networkAttachments, err := ovncontroller.CreateAdditionalNetworks(ctx, helper, instance, serviceLabels)
	if err != nil {
		Log.Info(fmt.Sprintf("Failed to create additional networks: %s", err))
		return ctrl.Result{}, err
	}

	// network to attach to
	networkAttachmentsNoPhysNet := []string{}
	if instance.Spec.NetworkAttachment != "" {
		networkAttachments = append(networkAttachments, instance.Spec.NetworkAttachment)
		networkAttachmentsNoPhysNet = append(networkAttachmentsNoPhysNet, instance.Spec.NetworkAttachment)
	}
	sort.Strings(networkAttachments)

	for _, netAtt := range networkAttachments {
		_, err = nad.GetNADWithName(ctx, helper, netAtt, instance.Namespace)
		if err != nil {
			if k8s_errors.IsNotFound(err) {
				instance.Status.Conditions.Set(condition.FalseCondition(
					condition.NetworkAttachmentsReadyCondition,
					condition.RequestedReason,
					condition.SeverityInfo,
					condition.NetworkAttachmentsReadyWaitingMessage,
					netAtt))
				return ctrl.Result{RequeueAfter: time.Second * 10}, fmt.Errorf("network-attachment-definition %s not found", netAtt)
			}
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.NetworkAttachmentsReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.NetworkAttachmentsReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
	}

	serviceAnnotations, err := nad.CreateNetworksAnnotation(instance.Namespace, networkAttachments)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create network annotation from %s: %w",
			networkAttachments, err)
	}

	// Handle service init
	ctrlResult, err := r.reconcileInit(ctx, instance, helper)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}

	// Handle service update
	ctrlResult, err = r.reconcileUpdate(ctx, instance, helper)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}

	// Handle service upgrade
	ctrlResult, err = r.reconcileUpgrade(ctx, instance, helper)
	if err != nil {
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		return ctrlResult, nil
	}

	// Handle OVN dbs TLS cert/key
	var tlsDeploymentResources *tls.DeploymentResources

	if instance.Spec.TLS != nil && instance.Spec.TLS.Service != nil {
		// generate certificate
		if instance.Spec.TLS.Service.IssuerName != nil {
			certRequest := certmanager.CertificateRequest{
				IssuerName: *instance.Spec.TLS.Service.IssuerName,
				CertName:   fmt.Sprintf("%s-svc", instance.Name),
				Duration:   nil,
				// Not the actual hostname but must provide something to generate the cert
				Hostnames: []string{
					fmt.Sprintf("%s.%s.svc", ovncontroller.ServiceName, instance.Namespace),
					fmt.Sprintf("%s.%s.svc.cluster.local", ovncontroller.ServiceName, instance.Namespace),
				},
				Ips:         nil,
				Annotations: map[string]string{},
				Labels:      serviceLabels,
				Usages: []certmgrv1.KeyUsage{
					certmgrv1.UsageClientAuth,
				},
			}
			certSecret, ctrlResult, err := certmanager.EnsureCert(
				ctx,
				helper,
				certRequest)
			if err != nil {
				return ctrlResult, err
			} else if (ctrlResult != ctrl.Result{}) {
				return ctrlResult, nil
			}

			values := [][]byte{}
			if certSecret != nil {
				for _, field := range []string{"tls.key", "tls.crt"} {
					val, ok := certSecret.Data[field]
					if !ok {
						return ctrl.Result{}, fmt.Errorf("field %s not found in Secret %s", field, certSecret.Name)
					}
					values = append(values, val)
				}
			}

			hash, err := util.ObjectHash(values)
			if err != nil {
				return ctrl.Result{}, err
			}

			tlsDeploymentResources = &tls.DeploymentResources{
				Volumes: []tls.Volume{
					{
						IsCA: false,
						Hash: hash,
						Volume: corev1.Volume{
							Name: fmt.Sprintf("tls-certs-%s", instance.Name),
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName:  certSecret.Name,
									DefaultMode: ptr.To[int32](0440),
								},
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      fmt.Sprintf("tls-certs-%s", instance.Name),
								MountPath: "/etc/pki/tls/certs/ovn_dbs.crt",
								SubPath:   "tls.crt",
								ReadOnly:  true,
							},
							{
								Name:      fmt.Sprintf("tls-certs-%s", instance.Name),
								MountPath: "/etc/pki/tls/private/ovn_dbs.key",
								SubPath:   "tls.key",
								ReadOnly:  true,
							},
							{
								Name:      fmt.Sprintf("tls-certs-%s", instance.Name),
								MountPath: "/etc/pki/tls/certs/ovn_dbs_ca.crt",
								SubPath:   "ca.crt",
								ReadOnly:  true,
							},
						},
					},
				},
			}
		}
	}

	// Define a new DaemonSet object
	ovnDaemonSet, err := ovncontroller.DaemonSet(instance, inputHash, serviceLabels, serviceAnnotations, tlsDeploymentResources)
	if err != nil {
		Log.Error(err, "Failed to create OVNController DaemonSet")
		return ctrl.Result{}, err
	}
	dset := daemonset.NewDaemonSet(
		ovnDaemonSet,
		time.Duration(5)*time.Second,
	)

	ctrlResult, err = dset.CreateOrPatch(ctx, helper)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.DeploymentReadyErrorMessage,
			err.Error()))
		return ctrlResult, err
	} else if (ctrlResult != ctrl.Result{}) {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.DeploymentReadyCondition,
			condition.RequestedReason,
			condition.SeverityInfo,
			condition.DeploymentReadyRunningMessage))
		return ctrlResult, nil
	}

	instance.Status.DesiredNumberScheduled = dset.GetDaemonSet().Status.DesiredNumberScheduled
	instance.Status.NumberReady = dset.GetDaemonSet().Status.NumberReady

	// verify if network attachment matches expectations
	networkReady, networkAttachmentStatus, err := nad.VerifyNetworkStatusFromAnnotation(ctx, helper, networkAttachmentsNoPhysNet, serviceLabels, instance.Status.NumberReady)
	if err != nil {
		return ctrl.Result{}, err
	}

	instance.Status.NetworkAttachments = networkAttachmentStatus
	if networkReady {
		instance.Status.Conditions.MarkTrue(condition.NetworkAttachmentsReadyCondition, condition.NetworkAttachmentsReadyMessage)
	} else {
		err := fmt.Errorf("not all pods have interfaces with ips as configured in NetworkAttachments: %s", instance.Spec.NetworkAttachment)
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.NetworkAttachmentsReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.NetworkAttachmentsReadyErrorMessage,
			err.Error()))

		return ctrl.Result{}, err
	}

	if instance.Status.NumberReady == instance.Status.DesiredNumberScheduled {
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)
	}
	// create DaemonSet - end

	sbCluster, err := v1beta1.GetDBClusterByType(ctx, helper, instance.Namespace, map[string]string{}, v1beta1.SBDBType)
	if err != nil {
		Log.Info("No SB OVNDBCluster defined, deleting external ConfigMap")
		cleanupConfigMapErr := r.deleteExternalConfigMaps(ctx, helper, instance)
		if cleanupConfigMapErr != nil {
			Log.Error(cleanupConfigMapErr, "Failed to delete external ConfigMap")
			return ctrl.Result{}, cleanupConfigMapErr
		}
		return ctrl.Result{}, nil
	}

	_, err = sbCluster.GetExternalEndpoint()
	if err != nil {
		Log.Info("No external endpoint defined for SB OVNDBCluster, deleting external ConfigMap")
		cleanupConfigMapErr := r.deleteExternalConfigMaps(ctx, helper, instance)
		if cleanupConfigMapErr != nil {
			Log.Error(cleanupConfigMapErr, "Failed to delete external ConfigMap")
			return ctrl.Result{}, cleanupConfigMapErr
		}
		return ctrl.Result{}, nil
	}

	// Create ConfigMap for external dataplane consumption
	// TODO(ihar) - is there any hashing mechanism for EDP config? do we trigger deploy somehow?
	err = r.generateExternalConfigMaps(ctx, helper, instance, sbCluster, &configMapVars)
	if err != nil {
		Log.Error(err, "Failed to generate external ConfigMap")
		return ctrl.Result{}, err
	}

	// create OVN Config Job - start
	if instance.Status.NumberReady == instance.Status.DesiredNumberScheduled {
		jobsDef, err := ovncontroller.ConfigJob(ctx, helper, r.Client, instance, sbCluster, serviceLabels)
		if err != nil {
			Log.Error(err, "Failed to create OVN controller configuration Job")
			return ctrl.Result{}, err
		}
		for _, jobDef := range jobsDef {
			configHashKey := v1beta1.OvnConfigHash + "-" + jobDef.Spec.Template.Spec.NodeName
			configHash := instance.Status.Hash[configHashKey]
			configJob := job.NewJob(
				jobDef,
				configHashKey,
				false,
				time.Duration(5)*time.Second,
				configHash,
			)
			ctrlResult, err = configJob.DoJob(ctx, helper)
			if (ctrlResult != ctrl.Result{}) {
				instance.Status.Conditions.Set(
					condition.FalseCondition(
						condition.ServiceConfigReadyCondition,
						condition.RequestedReason,
						condition.SeverityInfo,
						condition.ServiceConfigReadyMessage,
					),
				)
				return ctrlResult, nil
			}
			if err != nil {
				Log.Error(err, "Failed to configure OVN controller")
				instance.Status.Conditions.Set(
					condition.FalseCondition(
						condition.ServiceConfigReadyCondition,
						condition.RequestedReason,
						condition.SeverityInfo,
						condition.ServiceConfigReadyErrorMessage,
						err.Error(),
					),
				)
				return ctrl.Result{}, err
			}
			if configJob.HasChanged() {
				instance.Status.Hash[configHashKey] = configJob.GetHash()
				Log.Info(fmt.Sprintf("Job %s hash added - %s", jobDef.Name, instance.Status.Hash[configHashKey]))
			}
		}
		instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)
	} else {
		Log.Info("OVNController DaemonSet not ready yet. Configuration job cannot be started.")
		return ctrl.Result{Requeue: true}, nil
	}
	// create OVN Config Job - end

	Log.Info("Reconciled Service successfully")

	return ctrl.Result{}, nil
}

// generateServiceConfigMaps - create configmaps which hold scripts and service configuration
func (r *OVNControllerReconciler) generateServiceConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *v1beta1.OVNController,
	envVars *map[string]env.Setter,
) error {
	// Create/update configmaps from templates
	cmLabels := labels.GetLabels(instance, labels.GetGroupLabel(ovncontroller.ServiceName), map[string]string{})

	templateParameters := make(map[string]interface{})
	if instance.Spec.NetworkAttachment != "" {
		templateParameters["OvnEncapNIC"] = nad.GetNetworkIFName(instance.Spec.NetworkAttachment)
	} else {
		templateParameters["OvnEncapNIC"] = "eth0"
	}
	cms := []util.Template{
		// ScriptsConfigMap
		{
			Name:          fmt.Sprintf("%s-scripts", instance.Name),
			Namespace:     instance.Namespace,
			Type:          util.TemplateTypeScripts,
			InstanceType:  instance.Kind,
			Labels:        cmLabels,
			ConfigOptions: templateParameters,
		},
	}
	return configmap.EnsureConfigMaps(ctx, h, instance, cms, envVars)
}

// generateExternalConfigMaps - create configmaps for external dataplane consumption
func (r *OVNControllerReconciler) generateExternalConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *v1beta1.OVNController,
	sbCluster *v1beta1.OVNDBCluster,
	envVars *map[string]env.Setter,
) error {
	// Create/update configmaps from templates
	cmLabels := labels.GetLabels(instance, labels.GetGroupLabel(ovncontroller.ServiceName), map[string]string{})

	externalEndpoint, err := sbCluster.GetExternalEndpoint()
	if err != nil {
		return err
	}

	externalTemplateParameters := make(map[string]interface{})
	// TODO change externalEndpoint to DNS
	externalTemplateParameters["OvnRemote"] = externalEndpoint
	externalTemplateParameters["OvnEncapType"] = instance.Spec.ExternalIDS.OvnEncapType

	cms := []util.Template{
		// EDP ConfigMap
		{
			Name:          fmt.Sprintf("%s-config", instance.Name),
			Namespace:     instance.Namespace,
			Type:          util.TemplateTypeConfig,
			InstanceType:  instance.Kind,
			Labels:        cmLabels,
			ConfigOptions: externalTemplateParameters,
		},
	}
	return configmap.EnsureConfigMaps(ctx, h, instance, cms, envVars)
}

// TODO(ihar) this function could live in lib-common
// deleteExternalConfigMaps - delete obsolete configmaps for external dataplane consumption
func (r *OVNControllerReconciler) deleteExternalConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *v1beta1.OVNController,
) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-config", instance.Name),
			Namespace: instance.Namespace,
		},
	}

	err := h.GetClient().Delete(ctx, cm)
	if err != nil && !k8s_errors.IsNotFound(err) {
		return fmt.Errorf("error deleting external config map %s: %w", cm.Name, err)
	}
	return nil
}

// createHashOfInputHashes - creates a hash of hashes which gets added to the resources which requires a restart
// if any of the input resources change, like configs, passwords, ...
//
// returns the hash, whether the hash changed (as a bool) and any error
func (r *OVNControllerReconciler) createHashOfInputHashes(
	ctx context.Context,
	instance *v1beta1.OVNController,
	envVars map[string]env.Setter,
) (string, bool, error) {
	Log := r.GetLogger(ctx)

	var hashMap map[string]string
	changed := false
	mergedMapVars := env.MergeEnvs([]corev1.EnvVar{}, envVars)
	hash, err := util.ObjectHash(mergedMapVars)
	if err != nil {
		return hash, changed, err
	}
	if hashMap, changed = util.SetHash(instance.Status.Hash, common.InputHashName, hash); changed {
		instance.Status.Hash = hashMap
		Log.Info(fmt.Sprintf("Input maps hash %s - %s", common.InputHashName, hash))
	}
	return hash, changed, nil
}
