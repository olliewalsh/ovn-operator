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

package v1beta1

import (
	"github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	"github.com/openstack-k8s-operators/lib-common/modules/common/tls"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// OvnConfigHash - OvnConfigHash key
	OvnConfigHash = "OvnConfigHash"

	// Container image fall-back defaults

	// OvnControllerOvsContainerImage is the fall-back container image for OVNController ovs-*
	OvnControllerOvsContainerImage = "quay.io/podified-antelope-centos9/openstack-ovn-base:current-podified"
	// OvnControllerContainerImage is the fall-back container image for OVNController ovn-controller
	OvnControllerContainerImage = "quay.io/podified-antelope-centos9/openstack-ovn-controller:current-podified"
)

// OVNControllerSpec defines the desired state of OVNController
type OVNControllerSpec struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default={}
	ExternalIDS OVSExternalIDs `json:"external-ids"`

	// +kubebuilder:validation:Required
	// Image used for the ovsdb-server and ovs-vswitchd containers (will be set to environmental default if empty)
	OvsContainerImage string `json:"ovsContainerImage"`

	// +kubebuilder:validation:Required
	// Image used for the ovn-controller container (will be set to environmental default if empty)
	OvnContainerImage string `json:"ovnContainerImage"`

	// +kubebuilder:validation:Optional
	// Debug - enable debug for different deploy stages. If an init container is used, it runs and the
	// actual action pod gets started with sleep infinity
	Debug OVNControllerDebug `json:"debug,omitempty"`

	// +kubebuilder:validation:Optional
	// +optional
	NicMappings map[string]string `json:"nicMappings,omitempty"`

	// +kubebuilder:validation:Optional
	// Resources - Compute Resources required by this service (Limits/Requests).
	// https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:validation:Optional
	// NodeSelector to target subset of worker nodes running this service
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +kubebuilder:validation:Optional
	// NetworkAttachment is a NetworkAttachment resource name to expose the service to the given network.
	// If specified the IP address of this network is used as the OvnEncapIP.
	NetworkAttachment string `json:"networkAttachment"`

	// +kubebuilder:validation:Optional
	// TLS - Parameters related to the TLS
	TLS *tls.TLS `json:"tls,omitempty"`
}

// OVNControllerDebug defines the observed state of OVNControllerDebug
type OVNControllerDebug struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default=false
	// Service enable debug
	Service bool `json:"service"`
}

// OVNControllerStatus defines the observed state of OVNController
type OVNControllerStatus struct {
	// NumberReady of the OVNController instances
	NumberReady int32 `json:"numberReady,omitempty"`

	// DesiredNumberScheduled - total number of the nodes which should be running Daemon
	DesiredNumberScheduled int32 `json:"desiredNumberScheduled,omitempty"`

	// Conditions
	Conditions condition.Conditions `json:"conditions,omitempty" optional:"true"`

	// Map of hashes to track e.g. job status
	Hash map[string]string `json:"hash,omitempty"`

	// NetworkAttachments status of the deployment pods
	NetworkAttachments map[string][]string `json:"networkAttachments,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="NetworkAttachment",type="string",JSONPath=".spec.networkAttachment",description="NetworkAttachment"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[0].status",description="Status"
//+kubebuilder:printcolumn:name="Message",type="string",JSONPath=".status.conditions[0].message",description="Message"

// OVNController is the Schema for the ovncontrollers API
type OVNController struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OVNControllerSpec   `json:"spec,omitempty"`
	Status OVNControllerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// OVNControllerList contains a list of OVNController
type OVNControllerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OVNController `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OVNController{}, &OVNControllerList{})
}

// IsReady - returns true if service is ready to server requests
func (instance OVNController) IsReady() bool {
	// Ready when:
	// OVNController is reconciled successfully
	return instance.Status.Conditions.IsTrue(condition.ReadyCondition)
}

// OVSExternalIDs is a set of configuration options for OVS external-ids table
type OVSExternalIDs struct {
	// +kubebuilder:validation:Optional
	// +kubebuilder:default="random"
	SystemID string `json:"system-id,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="br-int"
	OvnBridge string `json:"ovn-bridge,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default="geneve"
	// +kubebuilder:validation:Enum={"geneve","vxlan"}
	OvnEncapType string `json:"ovn-encap-type,omitempty"`

	// +kubebuilder:validation:Optional
	// +kubebuilder:default=true
	EnableChassisAsGateway bool `json:"enable-chassis-as-gateway"`
}

// RbacConditionsSet - set the conditions for the rbac object
func (instance OVNController) RbacConditionsSet(c *condition.Condition) {
	instance.Status.Conditions.Set(c)
}

// RbacNamespace - return the namespace
func (instance OVNController) RbacNamespace() string {
	return instance.Namespace
}

// RbacResourceName - return the name to be used for rbac objects (serviceaccount, role, rolebinding)
func (instance OVNController) RbacResourceName() string {
	return "ovncontroller-" + instance.Name
}
