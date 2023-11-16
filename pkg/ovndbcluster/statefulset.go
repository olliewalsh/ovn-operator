/*
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

package ovndbcluster

import (
	"github.com/openstack-k8s-operators/lib-common/modules/common"
	"github.com/openstack-k8s-operators/lib-common/modules/common/affinity"
	"github.com/openstack-k8s-operators/lib-common/modules/common/env"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"
	"github.com/openstack-k8s-operators/ovn-operator/api/v1beta1"
	ovnv1 "github.com/openstack-k8s-operators/ovn-operator/api/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ServiceCommand -
	ServiceCommand = "/usr/local/bin/kolla_set_configs && /usr/local/bin/kolla_start"

	// PvcSuffixEtcOvn -
	PvcSuffixEtcOvn = "-etc-ovn"
)

// StatefulSet func
func StatefulSet(
	instance *ovnv1.OVNDBCluster,
	configHash string,
	labels map[string]string,
	annotations map[string]string,
	tlsDeploymentResources *tls.DeploymentResources,
) *appsv1.StatefulSet {
	runAsUser := int64(0)

	livenessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       3,
		InitialDelaySeconds: 3,
	}
	readinessProbe := &corev1.Probe{
		// TODO might need tuning
		TimeoutSeconds:      5,
		PeriodSeconds:       5,
		InitialDelaySeconds: 5,
	}

	noopCmd := []string{
		"/bin/true",
	}
	var preStopCmd []string
	var postStartCmd []string
	args := []string{"-c"}
	if instance.Spec.Debug.Service {
		args = append(args, common.DebugCommand)
		livenessProbe.Exec = &corev1.ExecAction{
			Command: noopCmd,
		}

		readinessProbe.Exec = &corev1.ExecAction{
			Command: noopCmd,
		}

		postStartCmd = noopCmd
		preStopCmd = noopCmd
	} else {
		args = append(args, ServiceCommand)
		//
		// https://kubernetes.io/docs/tasks/configure-pod-container/configure-liveness-readiness-startup-probes/
		//
		livenessProbe.Exec = &corev1.ExecAction{
			Command: []string{
				"/usr/bin/pidof", "ovsdb-server",
			},
		}
		readinessProbe.Exec = livenessProbe.Exec

		postStartCmd = []string{
			"/usr/local/bin/container-scripts/settings.sh",
		}
		preStopCmd = []string{
			"/usr/local/bin/container-scripts/cleanup.sh",
		}
	}

	lifecycle := &corev1.Lifecycle{
		PostStart: &corev1.LifecycleHandler{
			Exec: &corev1.ExecAction{
				Command: postStartCmd,
			},
		},
		PreStop: &corev1.LifecycleHandler{
			Exec: &corev1.ExecAction{
				Command: preStopCmd,
			},
		},
	}
	serviceName := ServiceNameNB
	if instance.Spec.DBType == v1beta1.SBDBType {
		serviceName = ServiceNameSB
	}

	// create Volume and VolumeMounts
	volumes := GetDBClusterVolumes(instance.Name)
	volumeMounts := GetDBClusterVolumeMounts(instance.Name + PvcSuffixEtcOvn)

	if len(tlsDeploymentResources.Volumes) != 0 {
		volumes = append(volumes, tlsDeploymentResources.GetVolumes(false)...)
		volumeMounts = append(volumeMounts, tlsDeploymentResources.GetVolumeMounts(false)...)
	}

	envVars := map[string]env.Setter{}
	envVars["KOLLA_CONFIG_STRATEGY"] = env.SetValue("COPY_ALWAYS")
	envVars["CONFIG_HASH"] = env.SetValue(configHash)
	// TODO: Make confs customizable
	envVars["OVN_RUNDIR"] = env.SetValue("/tmp")

	statefulset := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: instance.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			ServiceName: serviceName,
			Replicas:    instance.Spec.Replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: annotations,
					Labels:      labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: instance.RbacResourceName(),
					Containers: []corev1.Container{
						{
							Name: serviceName,
							Command: []string{
								"/bin/bash",
							},
							Args:  args,
							Image: instance.Spec.ContainerImage,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser: &runAsUser,
							},
							Env:                      env.MergeEnvs([]corev1.EnvVar{}, envVars),
							VolumeMounts:             volumeMounts,
							Resources:                instance.Spec.Resources,
							ReadinessProbe:           readinessProbe,
							LivenessProbe:            livenessProbe,
							Lifecycle:                lifecycle,
							TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	// https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/#persistentvolumeclaim-retention
	statefulset.Spec.PersistentVolumeClaimRetentionPolicy = &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{
		WhenDeleted: appsv1.DeletePersistentVolumeClaimRetentionPolicyType,
		WhenScaled:  appsv1.RetainPersistentVolumeClaimRetentionPolicyType,
	}

	blockOwnerDeletion := false
	ownerRef := metav1.NewControllerRef(instance, instance.GroupVersionKind())
	ownerRef.BlockOwnerDeletion = &blockOwnerDeletion

	statefulset.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:            instance.Name + PvcSuffixEtcOvn,
				Namespace:       instance.Namespace,
				Labels:          labels,
				OwnerReferences: []metav1.OwnerReference{*ownerRef},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					corev1.ReadWriteOnce,
				},
				StorageClassName: &instance.Spec.StorageClass,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(instance.Spec.StorageRequest),
					},
				},
			},
		},
	}
	// If possible two pods of the same service should not
	// run on the same worker node. If this is not possible
	// the get still created on the same worker node.
	statefulset.Spec.Template.Spec.Affinity = affinity.DistributePods(
		common.AppSelector,
		[]string{
			serviceName,
		},
		corev1.LabelHostname,
	)
	if instance.Spec.NodeSelector != nil && len(instance.Spec.NodeSelector) > 0 {
		statefulset.Spec.Template.Spec.NodeSelector = instance.Spec.NodeSelector
	}

	return statefulset
}
