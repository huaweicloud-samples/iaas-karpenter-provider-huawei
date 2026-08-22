/*
Copyright 2026.

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

package v1alpha1

import (
	"fmt"

	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// CCENodeClassSpec defines the desired state of CCENodeClass
type CCENodeClassSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// SubnetSelectorTerms selects VPC subnets for CCE node networking. The terms are ORed.
	// Huawei Cloud VPC subnets are not bound to an AZ.
	// See https://support.huaweicloud.com/api-vpc/vpc_subnet01_0002.html
	// +kubebuilder:validation:XValidation:message="subnetSelectorTerms cannot be empty",rule="self.size() != 0"
	// +kubebuilder:validation:XValidation:message="expected at least one, got none, ['name', 'id']",rule="self.all(x, has(x.name) || has(x.id))"
	// +kubebuilder:validation:MaxItems:=30
	// +required
	SubnetSelectorTerms []SubnetSelectorTerm `json:"subnetSelectorTerms" hash:"ignore"`

	// Kubelet defines the kubelet settings that can be translated into CCE CreateNode extendParam fields.
	// +optional
	Kubelet *KubeletConfiguration `json:"kubelet,omitempty"`

	// ECSGroupID is the ECS server group ID used when creating CCE nodes.
	// +optional
	ECSGroupID *string `json:"ecsGroupId,omitempty"`

	// IMSSelector selects the node operating system family.
	// +required
	IMSSelector IMSSelector `json:"imsSelector" hash:"ignore"`

	// BlockDeviceMappings defines the root, k8s, and user data volumes used for CCE CreateNode.
	// +required
	BlockDeviceMappings BlockDeviceMappings `json:"blockDeviceMappings" hash:"ignore"`

	// RuntimeConfiguration configures the container runtime used on the node.
	// +optional
	RuntimeConfiguration *RuntimeConfiguration `json:"runtimeConfiguration,omitempty" hash:"ignore"`

	// Login defines the node login configuration.
	// +required
	Login Login `json:"login" hash:"ignore"`

	// Tags are ECS resource tags applied to the server backing the node,
	// e.g. for cost allocation. Keys must not start with "CCE-" or
	// "__type_baremetal" (reserved by CCE).
	// +kubebuilder:validation:XValidation:message="tag keys must not start with 'CCE-' or '__type_baremetal'",rule="self.all(k, !k.startsWith('CCE-') && !k.startsWith('__type_baremetal'))"
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
}

type normalizedIMSSelection struct {
	Family string
}

type normalizedVolume struct {
	Size       int32
	VolumeType string
	Iops       int32
	Throughput int32
}

type normalizedRuntimeConfiguration struct {
	Type string
}

type normalizedUserPassword struct {
	Username string
	Password string
}

type normalizedLogin struct {
	UserPassword *normalizedUserPassword
	SSHKey       string
}

type normalizedBlockDeviceMappings struct {
	Root  normalizedVolume
	K8S   *normalizedVolume
	Users []normalizedVolume
}

const (
	MinRootVolumeSizeGiB           = int32(40)
	MinKubernetesDataVolumeSizeGiB = int32(20)
	MinUserDataVolumeSizeGiB       = int32(10)
	DefaultKubernetesVolumeSizeGiB = int32(100)
)

// IMSSelector defines the node operating system family used by CCE CreateNode.
type IMSSelector struct {
	// IMSFamily is the node operating system family.
	// +kubebuilder:validation:MinLength=1
	IMSFamily string `json:"imsFamily"`
}

// BlockDeviceMappings defines disk configuration for root, k8s, and user data volumes.
// +kubebuilder:validation:XValidation:message="blockDeviceMappings.root.volumeSize must be at least 40Gi",rule="self.root.volumeSize >= 40"
// +kubebuilder:validation:XValidation:message="blockDeviceMappings.k8s.volumeSize must be at least 20Gi when specified",rule="!has(self.k8s) || self.k8s.volumeSize >= 20"
// +kubebuilder:validation:XValidation:message="blockDeviceMappings.users volumeSize must be at least 10Gi",rule="!has(self.users) || self.users.all(x, x.volumeSize >= 10)"
type BlockDeviceMappings struct {
	// K8S is the data volume used by the container runtime and kubelet.
	// +optional
	K8S *BlockDevice `json:"k8s,omitempty"`

	// Root is the system disk.
	// +required
	Root BlockDevice `json:"root"`

	// Users are additional user data disks.
	// +optional
	Users []BlockDevice `json:"users,omitempty"`
}

// BlockDevice defines a CCE volume.
type BlockDevice struct {
	// VolumeSize is the disk size in GiB.
	// +kubebuilder:validation:Minimum:=10
	VolumeSize int32 `json:"volumeSize"`

	// VolumeType is the disk type.
	// +kubebuilder:validation:MinLength=1
	VolumeType string `json:"volumeType"`

	// IOPS is required for GPSSD2 and ESSD2.
	// +optional
	IOPS *int32 `json:"iops,omitempty"`

	// Throughput is required for GPSSD2.
	// +optional
	Throughput *int32 `json:"throughput,omitempty"`
}

// RuntimeConfiguration defines the node container runtime.
type RuntimeConfiguration struct {
	// Type is the container runtime type.
	// +kubebuilder:validation:Enum=docker;containerd
	// +optional
	Type string `json:"type,omitempty"`
}

// Login defines the node login configuration.
// +kubebuilder:validation:XValidation:message="exactly one of login.userPassword or login.sshKey must be set",rule="(has(self.userPassword) && !has(self.sshKey)) || (!has(self.userPassword) && has(self.sshKey))"
type Login struct {
	// UserPassword defines username/password login.
	// +optional
	UserPassword *UserPassword `json:"userPassword,omitempty"`

	// SSHKey is the name of an existing Huawei Cloud key pair for SSH login.
	// +kubebuilder:validation:MinLength=1
	// +optional
	SSHKey string `json:"sshKey,omitempty"`
}

// UserPassword defines the node login credentials.
type UserPassword struct {
	// Username defaults to root.
	// +kubebuilder:validation:Enum=root
	// +optional
	Username string `json:"username,omitempty"`

	// Password is the salted and encrypted node password.
	// +kubebuilder:validation:MinLength=1
	Password string `json:"password"`
}

// ResolveIMSForCreateNode resolves the node OS selection for CCE CreateNode.
func (s *CCENodeClassSpec) ResolveIMSForCreateNode() (osAlias string, err error) {
	if s == nil {
		return "", fmt.Errorf("nodeClass.spec is nil")
	}
	osAlias = s.IMSSelector.IMSFamily
	if osAlias == "" {
		return "", fmt.Errorf("nodeClass.spec.imsSelector.imsFamily is required")
	}
	return osAlias, nil
}

func (s *CCENodeClassSpec) ValidateForCreateNode() error {
	if s == nil {
		return fmt.Errorf("nodeClass.spec is nil")
	}
	hasUserPassword := s.Login.UserPassword != nil
	hasSSHKey := s.Login.SSHKey != ""
	if hasUserPassword == hasSSHKey {
		return fmt.Errorf("exactly one of nodeClass.spec.login.userPassword or nodeClass.spec.login.sshKey must be set")
	}
	if hasUserPassword && s.Login.UserPassword.Password == "" {
		return fmt.Errorf("nodeClass.spec.login.userPassword.password is required")
	}
	if s.BlockDeviceMappings.Root.VolumeSize < MinRootVolumeSizeGiB {
		return fmt.Errorf("nodeClass.spec.blockDeviceMappings.root.volumeSize must be at least %dGi", MinRootVolumeSizeGiB)
	}
	if s.BlockDeviceMappings.K8S != nil && s.BlockDeviceMappings.K8S.VolumeSize < MinKubernetesDataVolumeSizeGiB {
		return fmt.Errorf("nodeClass.spec.blockDeviceMappings.k8s.volumeSize must be at least %dGi", MinKubernetesDataVolumeSizeGiB)
	}
	for i, user := range s.BlockDeviceMappings.Users {
		if user.VolumeSize < MinUserDataVolumeSizeGiB {
			return fmt.Errorf("nodeClass.spec.blockDeviceMappings.users[%d].volumeSize must be at least %dGi", i, MinUserDataVolumeSizeGiB)
		}
	}
	return nil
}

func (s *CCENodeClassSpec) normalizedIMSSelection() normalizedIMSSelection {
	if s == nil {
		return normalizedIMSSelection{}
	}
	return normalizedIMSSelection{
		Family: s.IMSSelector.IMSFamily,
	}
}

func normalizeBlockDevice(device *BlockDevice) normalizedVolume {
	if device == nil {
		return normalizedVolume{}
	}
	size := device.VolumeSize
	if size <= 0 {
		size = 40
	}
	volumeType := device.VolumeType
	if volumeType == "" {
		volumeType = "SSD"
	}
	return normalizedVolume{
		Size:       size,
		VolumeType: volumeType,
		Iops:       lo.FromPtrOr(device.IOPS, 0),
		Throughput: lo.FromPtrOr(device.Throughput, 0),
	}
}

func (s *CCENodeClassSpec) normalizedBlockDeviceMappings() normalizedBlockDeviceMappings {
	if s == nil {
		return normalizedBlockDeviceMappings{}
	}
	users := make([]normalizedVolume, 0, len(s.BlockDeviceMappings.Users))
	for _, user := range s.BlockDeviceMappings.Users {
		users = append(users, normalizeBlockDevice(&user))
	}
	var k8s *normalizedVolume
	if s.BlockDeviceMappings.K8S != nil {
		n := normalizeBlockDevice(s.BlockDeviceMappings.K8S)
		k8s = &n
	}
	return normalizedBlockDeviceMappings{
		Root:  normalizeBlockDevice(&s.BlockDeviceMappings.Root),
		K8S:   k8s,
		Users: users,
	}
}

func (s *CCENodeClassSpec) normalizedRuntimeConfiguration() normalizedRuntimeConfiguration {
	runtimeType := "containerd"
	if s != nil && s.RuntimeConfiguration != nil && s.RuntimeConfiguration.Type != "" {
		runtimeType = s.RuntimeConfiguration.Type
	}
	return normalizedRuntimeConfiguration{Type: runtimeType}
}

func (s *CCENodeClassSpec) normalizedLogin() normalizedLogin {
	if s == nil {
		return normalizedLogin{}
	}
	if s.Login.SSHKey != "" {
		return normalizedLogin{
			SSHKey: s.Login.SSHKey,
		}
	}
	if s.Login.UserPassword == nil {
		return normalizedLogin{}
	}
	username := s.Login.UserPassword.Username
	if username == "" {
		username = "root"
	}
	return normalizedLogin{
		UserPassword: &normalizedUserPassword{
			Username: username,
			Password: s.Login.UserPassword.Password,
		},
	}
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// CCENodeClass is the Schema for the ccenodeclasses API
type CCENodeClass struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of CCENodeClass
	// +required
	Spec CCENodeClassSpec `json:"spec"`

	// status defines the observed state of CCENodeClass
	// +optional
	Status CCENodeClassStatus `json:"status,omitempty"`
}

// We need to bump the CCENodeClassHashVersion when we make an update to the CCENodeClass CRD under these conditions:
// 1. A field changes its default value for an existing field that is already hashed
// 2. A field is added to the hash calculation with an already-set value
// 3. A field is removed from the hash calculations
const CCENodeClassHashVersion = "v3"

func (in *CCENodeClass) Hash() string {
	return fmt.Sprint(lo.Must(hashstructure.Hash(struct {
		Spec                         CCENodeClassSpec
		EffectiveIMS                 normalizedIMSSelection
		EffectiveBlockDeviceMappings normalizedBlockDeviceMappings
		EffectiveRuntime             normalizedRuntimeConfiguration
		EffectiveLogin               normalizedLogin
	}{
		Spec:                         in.Spec,
		EffectiveIMS:                 in.Spec.normalizedIMSSelection(),
		EffectiveBlockDeviceMappings: in.Spec.normalizedBlockDeviceMappings(),
		EffectiveRuntime:             in.Spec.normalizedRuntimeConfiguration(),
		EffectiveLogin:               in.Spec.normalizedLogin(),
	}, hashstructure.FormatV2, &hashstructure.HashOptions{
		SlicesAsSets:    true,
		IgnoreZeroValue: true,
		ZeroNil:         true,
	})))
}

// +kubebuilder:object:root=true

// CCENodeClassList contains a list of CCENodeClass
type CCENodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CCENodeClass `json:"items"`
}

// SubnetSelectorTerm defines selection logic for a subnet used by Karpenter to launch nodes.
// If multiple fields are used for selection, the requirements are ANDed.
type SubnetSelectorTerm struct {
	// ID is the subnet id in ECS
	// +kubebuilder:validation:Pattern="^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"
	// +optional
	ID string `json:"id,omitempty"`
	// Name is the subnet id in ECS
	// +kubebuilder:validation:MinLength=1
	// +optional
	Name string `json:"name,omitempty"`
}

// SecurityGroupSelectorTerm defines selection logic for a security group used by Karpenter to launch nodes.
// If multiple fields are used for selection, the requirements are ANDed.
type SecurityGroupSelectorTerm struct {
	// Tags is a map of key/value tags used to select security groups.
	// Specifying '*' for a value selects all values for a given tag key.
	// +kubebuilder:validation:XValidation:message="empty tag keys or values aren't supported",rule="self.all(k, k != '' && self[k] != '')"
	// +kubebuilder:validation:MaxProperties:=20
	// +optional
	Tags map[string]string `json:"tags,omitempty"`
	// ID is the security group id in ECS
	// +kubebuilder:validation:Pattern:="sg-[0-9a-z]+"
	// +optional
	ID string `json:"id,omitempty"`
	// Name is the security group name in ECS.
	// This value is the name field, which is different from the name tag.
	Name string `json:"name,omitempty"`
}

// KubeletConfiguration defines the kubelet settings that can be translated into CCE CreateNode extendParam fields.
type KubeletConfiguration struct {
	// MaxPods is an override for the maximum number of pods that can run on
	// a worker node instance. This maps to CCE CreateNode extendParam.maxPods.
	// +kubebuilder:validation:Minimum:=16
	// +kubebuilder:validation:Maximum:=256
	// +optional
	MaxPods *int32 `json:"maxPods,omitempty"`
	// SystemReserved contains resources reserved for OS system daemons and kernel memory.
	// Supported keys map to CCE CreateNode systemReserved* extendParam fields.
	// +kubebuilder:validation:XValidation:message="valid keys for systemReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="systemReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	SystemReserved map[string]string `json:"systemReserved,omitempty"`
	// KubeReserved contains resources reserved for Kubernetes system components.
	// Supported keys map to CCE CreateNode kubeReserved* extendParam fields.
	// +kubebuilder:validation:XValidation:message="valid keys for kubeReserved are ['cpu','memory','ephemeral-storage','pid']",rule="self.all(x, x=='cpu' || x=='memory' || x=='ephemeral-storage' || x=='pid')"
	// +kubebuilder:validation:XValidation:message="kubeReserved value cannot be a negative resource quantity",rule="self.all(x, !self[x].startsWith('-'))"
	// +optional
	KubeReserved map[string]string `json:"kubeReserved,omitempty"`
}

func (in *CCENodeClass) KubeletConfiguration() *KubeletConfiguration {
	return in.Spec.Kubelet
}

func (in *CCENodeClass) RuntimeConfiguration() *RuntimeConfiguration {
	return in.Spec.RuntimeConfiguration
}

func (in *CCENodeClass) BlockDeviceMappings() BlockDeviceMappings {
	if in == nil {
		return BlockDeviceMappings{}
	}
	return in.Spec.BlockDeviceMappings
}
