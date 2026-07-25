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

package instance

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/sdkerr"
	cceMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"
	vpcMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/vpc/v2/model"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
	sdk "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/huawei"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/subnet"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/utils"
)

func TestNodeIDFromProviderID(t *testing.T) {
	cases := []struct {
		name       string
		providerID string
		want       string
		wantErr    bool
	}{
		{
			name:       "RawUUID",
			providerID: "123e4567-e89b-12d3-a456-426614174000",
			want:       "123e4567-e89b-12d3-a456-426614174000",
		},
		{
			name:       "RawUUIDWithSpacesIsPreserved",
			providerID: "  123e4567-e89b-12d3-a456-426614174000  ",
			want:       "  123e4567-e89b-12d3-a456-426614174000  ",
		},
		{
			name:       "Empty",
			providerID: "",
			wantErr:    true,
		},
		{
			name:       "WhitespaceOnlyIsPreserved",
			providerID: "   ",
			want:       "   ",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nodeIDFromProviderID(tt.providerID)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("nodeIDFromProviderID() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestInstanceFromCCENodeParts_PopulatesSubnetID(t *testing.T) {
	instance := instanceFromCCENodeParts(
		&cceMdl.NodeMetadata{Uid: lo.ToPtr("node-123")},
		&cceMdl.NodeSpec{
			Flavor: "c9.large.2",
			Az:     "ap-southeast-3a",
			NodeNicSpec: &cceMdl.NodeNicSpec{
				PrimaryNic: &cceMdl.NicSpec{
					SubnetId: lo.ToPtr("subnet-123"),
				},
			},
		},
		&cceMdl.NodeStatus{ServerId: lo.ToPtr("server-123")},
	)

	if instance == nil {
		t.Fatalf("expected instance to be returned")
	}
	if instance.NodeID != "node-123" {
		t.Fatalf("expected node id %q, got %q", "node-123", instance.NodeID)
	}
	if instance.ServerID != "server-123" {
		t.Fatalf("expected server id %q, got %q", "server-123", instance.ServerID)
	}
	if instance.SubnetID != "subnet-123" {
		t.Fatalf("expected subnet id %q, got %q", "subnet-123", instance.SubnetID)
	}
}

func TestInstanceFromCCENodeParts_HandlesMissingNodeNicSpec(t *testing.T) {
	instance := instanceFromCCENodeParts(
		&cceMdl.NodeMetadata{Uid: lo.ToPtr("node-123")},
		&cceMdl.NodeSpec{
			Flavor: "c9.large.2",
			Az:     "ap-southeast-3a",
		},
		nil,
	)

	if instance == nil {
		t.Fatalf("expected instance to be returned")
	}
	if instance.SubnetID != "" {
		t.Fatalf("expected empty subnet id when node nic spec is missing, got %q", instance.SubnetID)
	}
}

func TestGetMapsCCENodeResponse(t *testing.T) {
	provider := &DefaultProvider{
		clusterID: "cluster-id",
		cceapi: &stubCCEAPI{
			showNodeResp: &cceMdl.ShowNodeResponse{
				Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-123")},
				Spec: &cceMdl.NodeSpec{
					Flavor: "c9.large.2",
					Az:     "ap-southeast-3a",
					NodeNicSpec: &cceMdl.NodeNicSpec{
						PrimaryNic: &cceMdl.NicSpec{SubnetId: lo.ToPtr("subnet-123")},
					},
				},
				Status: &cceMdl.NodeStatus{ServerId: lo.ToPtr("server-123")},
			},
		},
	}

	got, err := provider.Get(context.Background(), "node-123")
	if err != nil {
		t.Fatalf("expected get to succeed, got %v", err)
	}
	if got.NodeID != "node-123" || got.ServerID != "server-123" || got.Flavor != "c9.large.2" || got.Zone != "ap-southeast-3a" || got.SubnetID != "subnet-123" {
		t.Fatalf("expected CCE node fields to be mapped, got %#v", got)
	}
}

func TestListFiltersNodesWithoutNodeID(t *testing.T) {
	provider := &DefaultProvider{
		clusterID: "cluster-id",
		cceapi: &stubCCEAPI{
			listNodesResp: &cceMdl.ListNodesResponse{
				Items: lo.ToPtr([]cceMdl.Node{
					{Metadata: &cceMdl.NodeMetadata{}},
					{
						Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-123")},
						Spec:     &cceMdl.NodeSpec{Flavor: "c9.large.2", Az: "ap-southeast-3a"},
					},
				}),
			},
		},
	}

	got, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("expected list to succeed, got %v", err)
	}
	if len(got) != 1 || got[0].NodeID != "node-123" {
		t.Fatalf("expected only node with an id to be returned, got %#v", got)
	}
}

func TestBuildCreateCandidates_SortedStable(t *testing.T) {
	onDemandReqs := scheduling.NewRequirements(
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
	)

	newOffering := func(zone string) *cloudprovider.Offering {
		return &cloudprovider.Offering{
			Available: true,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
			),
		}
	}

	itB := &cloudprovider.InstanceType{
		Name: "it-b",
		Offerings: cloudprovider.Offerings{
			newOffering("zone-b"),
			newOffering("zone-a"),
		},
	}
	itA := &cloudprovider.InstanceType{
		Name: "it-a",
		Offerings: cloudprovider.Offerings{
			newOffering("zone-b"),
			newOffering("zone-a"),
		},
	}

	selectedSubnet := &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100}

	candidates := buildCreateCandidates([]*cloudprovider.InstanceType{itB, itA}, onDemandReqs, selectedSubnet)
	if len(candidates) != 4 {
		t.Fatalf("expected 4 candidates, got %d", len(candidates))
	}
	got := []string{
		candidates[0].zone + "/" + candidates[0].instanceType.Name,
		candidates[1].zone + "/" + candidates[1].instanceType.Name,
		candidates[2].zone + "/" + candidates[2].instanceType.Name,
		candidates[3].zone + "/" + candidates[3].instanceType.Name,
	}
	want := []string{"zone-a/it-a", "zone-a/it-b", "zone-b/it-a", "zone-b/it-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected order %v, got %v", want, got)
		}
	}
	for _, candidate := range candidates {
		if candidate.subnetID != selectedSubnet.ID {
			t.Fatalf("expected all zones to use subnet %q, got %#v", selectedSubnet.ID, candidates)
		}
	}
}

func TestBuildCreateCandidates_PrefersLowerPrice(t *testing.T) {
	onDemandReqs := scheduling.NewRequirements(
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
	)

	newOffering := func(zone string, price float64) *cloudprovider.Offering {
		return &cloudprovider.Offering{
			Available: true,
			Price:     price,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
			),
		}
	}

	ac7 := &cloudprovider.InstanceType{
		Name: "ac7.2xlarge.1",
		Offerings: cloudprovider.Offerings{
			newOffering("zone-a", 1.8402),
		},
	}
	x1 := &cloudprovider.InstanceType{
		Name: "x1.6u.10g",
		Offerings: cloudprovider.Offerings{
			newOffering("zone-a", 1.4951),
		},
	}

	selectedSubnet := &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100}

	candidates := buildCreateCandidates([]*cloudprovider.InstanceType{ac7, x1}, onDemandReqs, selectedSubnet)
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
	if candidates[0].instanceType.Name != "x1.6u.10g" {
		t.Fatalf("expected cheaper instance type first, got %q", candidates[0].instanceType.Name)
	}
	if candidates[1].instanceType.Name != "ac7.2xlarge.1" {
		t.Fatalf("expected more expensive instance type second, got %q", candidates[1].instanceType.Name)
	}
}

func TestNodeSpecForCandidate_MapsNewNodeClassFields(t *testing.T) {
	rootIOPS := int32(3000)
	rootThroughput := int32(125)
	k8sIOPS := int32(4000)
	userVolumeSize := int32(100)
	ecsGroupID := "46ebaf04-ca42-48ca-8057-0b96e6126e1b"
	maxPods := int32(64)
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			ECSGroupID:  &ecsGroupID,
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "HCE OS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{
					VolumeSize: 120,
					VolumeType: "GPSSD2",
					IOPS:       &rootIOPS,
					Throughput: &rootThroughput,
				},
				K8S: &v1alpha1.BlockDevice{
					VolumeSize: 120,
					VolumeType: "SAS",
					IOPS:       &k8sIOPS,
				},
				Users: []v1alpha1.BlockDevice{{
					VolumeSize: userVolumeSize,
					VolumeType: "SATA",
				}},
			},
			RuntimeConfiguration: &v1alpha1.RuntimeConfiguration{Type: "docker"},
			Kubelet: &v1alpha1.KubeletConfiguration{
				MaxPods: &maxPods,
				KubeReserved: map[string]string{
					string(corev1.ResourceCPU):              "1500m",
					string(corev1.ResourceMemory):           "1Gi",
					string(corev1.ResourceEphemeralStorage): "2Gi",
					"pid":                                   "1234",
				},
				SystemReserved: map[string]string{
					string(corev1.ResourceCPU):              "250m",
					string(corev1.ResourceMemory):           "512Mi",
					string(corev1.ResourceEphemeralStorage): "1Gi",
					"pid":                                   "4321",
				},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{
					Password: "ciphertext",
				},
			},
		},
	}
	nodeClaim := &karpv1.NodeClaim{}
	spec, err := nodeSpecForCandidate(
		nodeClass,
		nodeClaim,
		createCandidate{
			instanceType: &cloudprovider.InstanceType{Name: "c9.large.2"},
			zone:         "ap-southeast-3a",
			subnetID:     "subnet-123",
		},
		"HCE OS 2.0",
	)
	if err != nil {
		t.Fatalf("expected node spec creation to succeed, got %v", err)
	}
	if spec.RootVolume == nil {
		t.Fatalf("expected root volume to be set")
	}
	if spec.RootVolume.Size != 120 || spec.RootVolume.Volumetype != "GPSSD2" {
		t.Fatalf("expected root volume 50/GPSSD, got %#v", spec.RootVolume)
	}
	if spec.RootVolume.Iops == nil || *spec.RootVolume.Iops != rootIOPS {
		t.Fatalf("expected root volume iops %d, got %#v", rootIOPS, spec.RootVolume.Iops)
	}
	if spec.RootVolume.Throughput == nil || *spec.RootVolume.Throughput != rootThroughput {
		t.Fatalf("expected root volume throughput %d, got %#v", rootThroughput, spec.RootVolume.Throughput)
	}
	if spec.DataVolumes == nil || len(*spec.DataVolumes) != 2 {
		t.Fatalf("expected k8s and user data volumes, got %#v", spec.DataVolumes)
	}
	if got := (*spec.DataVolumes)[0]; got.Size != 120 || got.Volumetype != "SAS" {
		t.Fatalf("expected first data volume 120/SAS, got %#v", got)
	}
	if got := (*spec.DataVolumes)[1]; got.Size != userVolumeSize || got.Volumetype != "SATA" {
		t.Fatalf("expected second data volume 100/SATA, got %#v", got)
	}
	if spec.Storage == nil {
		t.Fatalf("expected storage to be set")
	}
	if len(spec.Storage.StorageSelectors) != 1 {
		t.Fatalf("expected one storage selector, got %#v", spec.Storage.StorageSelectors)
	}
	selector := spec.Storage.StorageSelectors[0]
	if selector.Name != storageSelectorName || selector.StorageType != "evs" {
		t.Fatalf("expected k8s storage selector, got %#v", selector)
	}
	if selector.MatchLabels == nil {
		t.Fatalf("expected selector match labels to be set")
	}
	if selector.MatchLabels.Size == nil || *selector.MatchLabels.Size != "120" {
		t.Fatalf("expected selector size 120, got %#v", selector.MatchLabels.Size)
	}
	if selector.MatchLabels.VolumeType == nil || *selector.MatchLabels.VolumeType != "SAS" {
		t.Fatalf("expected selector volume type SAS, got %#v", selector.MatchLabels.VolumeType)
	}
	if selector.MatchLabels.Iops == nil || *selector.MatchLabels.Iops != "4000" {
		t.Fatalf("expected selector iops 4000, got %#v", selector.MatchLabels.Iops)
	}
	if selector.MatchLabels.Count == nil || *selector.MatchLabels.Count != "1" {
		t.Fatalf("expected selector count 1, got %#v", selector.MatchLabels.Count)
	}
	if len(spec.Storage.StorageGroups) != 1 {
		t.Fatalf("expected one storage group, got %#v", spec.Storage.StorageGroups)
	}
	group := spec.Storage.StorageGroups[0]
	if group.Name != storageGroupName {
		t.Fatalf("expected storage group %q, got %#v", storageGroupName, group.Name)
	}
	if group.CceManaged == nil || !*group.CceManaged {
		t.Fatalf("expected storage group to be cce-managed, got %#v", group.CceManaged)
	}
	if len(group.SelectorNames) != 1 || group.SelectorNames[0] != storageSelectorName {
		t.Fatalf("expected storage group to reference selector %q, got %#v", storageSelectorName, group.SelectorNames)
	}
	if len(group.VirtualSpaces) != 2 {
		t.Fatalf("expected runtime and kubernetes virtual spaces, got %#v", group.VirtualSpaces)
	}
	if got := group.VirtualSpaces[0]; got.Name != "runtime" || got.Size != defaultRuntimeStorageSize || got.RuntimeConfig == nil || got.RuntimeConfig.LvType != defaultStorageLVType {
		t.Fatalf("expected runtime virtual space to use default layout, got %#v", got)
	}
	if got := group.VirtualSpaces[1]; got.Name != "kubernetes" || got.Size != defaultKubernetesStorageSize || got.LvmConfig == nil || got.LvmConfig.LvType != defaultStorageLVType {
		t.Fatalf("expected kubernetes virtual space to use default layout, got %#v", got)
	}
	if spec.Runtime == nil || spec.Runtime.Name == nil || spec.Runtime.Name.Value() != "docker" {
		t.Fatalf("expected docker runtime, got %#v", spec.Runtime)
	}
	if spec.Login == nil || spec.Login.UserPassword == nil {
		t.Fatalf("expected login user password to be set")
	}
	if spec.Login.UserPassword.Username == nil || *spec.Login.UserPassword.Username != "root" {
		t.Fatalf("expected default login username root, got %#v", spec.Login.UserPassword.Username)
	}
	if spec.Login.UserPassword.Password != "ciphertext" {
		t.Fatalf("expected login password to be propagated, got %#v", spec.Login.UserPassword.Password)
	}
	if spec.Login.SshKey != nil {
		t.Fatalf("expected ssh key to be omitted for password login, got %#v", spec.Login.SshKey)
	}
	if spec.EcsGroupId == nil || *spec.EcsGroupId != ecsGroupID {
		t.Fatalf("expected ecsGroupId %q, got %#v", ecsGroupID, spec.EcsGroupId)
	}
	if spec.Os == nil || *spec.Os != "HCE OS 2.0" {
		t.Fatalf("expected os %q, got %#v", "HCE OS 2.0", spec.Os)
	}
	if spec.ExtendParam == nil {
		t.Fatalf("expected extendParam to be set")
	}
	if spec.ExtendParam.MaxPods == nil || *spec.ExtendParam.MaxPods != maxPods {
		t.Fatalf("expected maxPods %d, got %#v", maxPods, spec.ExtendParam.MaxPods)
	}
	if spec.ExtendParam.KubeReservedCpu == nil || *spec.ExtendParam.KubeReservedCpu != 1500 {
		t.Fatalf("expected kubeReservedCpu 1500, got %#v", spec.ExtendParam.KubeReservedCpu)
	}
	if spec.ExtendParam.KubeReservedMem == nil || *spec.ExtendParam.KubeReservedMem != 1024 {
		t.Fatalf("expected kubeReservedMem 1024, got %#v", spec.ExtendParam.KubeReservedMem)
	}
	if spec.ExtendParam.KubeReservedStorage == nil || *spec.ExtendParam.KubeReservedStorage != 2 {
		t.Fatalf("expected kubeReservedStorage 2, got %#v", spec.ExtendParam.KubeReservedStorage)
	}
	if spec.ExtendParam.KubeReservedPid == nil || *spec.ExtendParam.KubeReservedPid != 1234 {
		t.Fatalf("expected kubeReservedPid 1234, got %#v", spec.ExtendParam.KubeReservedPid)
	}
	if spec.ExtendParam.SystemReservedCpu == nil || *spec.ExtendParam.SystemReservedCpu != 250 {
		t.Fatalf("expected systemReservedCpu 250, got %#v", spec.ExtendParam.SystemReservedCpu)
	}
	if spec.ExtendParam.SystemReservedMem == nil || *spec.ExtendParam.SystemReservedMem != 512 {
		t.Fatalf("expected systemReservedMem 512, got %#v", spec.ExtendParam.SystemReservedMem)
	}
	if spec.ExtendParam.SystemReservedStorage == nil || *spec.ExtendParam.SystemReservedStorage != 1 {
		t.Fatalf("expected systemReservedStorage 1, got %#v", spec.ExtendParam.SystemReservedStorage)
	}
	if spec.ExtendParam.SystemReservedPid == nil || *spec.ExtendParam.SystemReservedPid != 4321 {
		t.Fatalf("expected systemReservedPid 4321, got %#v", spec.ExtendParam.SystemReservedPid)
	}
}

func TestNodeSpecForCandidate_DefaultsManagedK8SDataDisk(t *testing.T) {
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "HCE OS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{
					VolumeSize: 120,
					VolumeType: "SAS",
				},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{
					Password: "ciphertext",
				},
			},
		},
	}

	spec, err := nodeSpecForCandidate(
		nodeClass,
		&karpv1.NodeClaim{},
		createCandidate{
			instanceType: &cloudprovider.InstanceType{Name: "c9.large.2"},
			zone:         "ap-southeast-3a",
			subnetID:     "subnet-123",
		},
		"HCE OS 2.0",
	)
	if err != nil {
		t.Fatalf("expected node spec creation to succeed, got %v", err)
	}

	if spec.DataVolumes == nil || len(*spec.DataVolumes) != 1 {
		t.Fatalf("expected one default k8s data volume, got %#v", spec.DataVolumes)
	}
	if got := (*spec.DataVolumes)[0]; got.Size != v1alpha1.DefaultKubernetesVolumeSizeGiB || got.Volumetype != "SAS" {
		t.Fatalf("expected default managed data volume 100/SAS, got %#v", got)
	}
	if spec.Storage == nil || len(spec.Storage.StorageSelectors) != 1 {
		t.Fatalf("expected storage selector for default k8s volume, got %#v", spec.Storage)
	}
	selector := spec.Storage.StorageSelectors[0]
	if selector.MatchLabels == nil || selector.MatchLabels.Size == nil || *selector.MatchLabels.Size != "100" {
		t.Fatalf("expected default selector size 100, got %#v", selector.MatchLabels)
	}
	if selector.MatchLabels.VolumeType == nil || *selector.MatchLabels.VolumeType != "SAS" {
		t.Fatalf("expected default selector volume type SAS, got %#v", selector.MatchLabels.VolumeType)
	}
	if spec.ExtendParam != nil {
		t.Fatalf("expected extendParam to be omitted when kubelet is unset, got %#v", spec.ExtendParam)
	}
}

func TestNodeSpecForCandidate_RejectsInvalidKubeletReservation(t *testing.T) {
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "HCE OS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{
					VolumeSize: 120,
					VolumeType: "SAS",
				},
			},
			Kubelet: &v1alpha1.KubeletConfiguration{
				KubeReserved: map[string]string{
					string(corev1.ResourceMemory): "1536Ki",
				},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{
					Password: "ciphertext",
				},
			},
		},
	}

	_, err := nodeSpecForCandidate(
		nodeClass,
		&karpv1.NodeClaim{},
		createCandidate{
			instanceType: &cloudprovider.InstanceType{Name: "c9.large.2"},
			zone:         "ap-southeast-3a",
			subnetID:     "subnet-123",
		},
		"HCE OS 2.0",
	)
	if err == nil {
		t.Fatalf("expected invalid kubelet reservation to be rejected")
	}
}

func TestNodeSpecForCandidate_UsesSSHKeyLogin(t *testing.T) {
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "HCE OS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{
					VolumeSize: 120,
					VolumeType: "SAS",
				},
			},
			Login: v1alpha1.Login{
				SSHKey: "cluster-keypair",
			},
		},
	}

	spec, err := nodeSpecForCandidate(
		nodeClass,
		&karpv1.NodeClaim{},
		createCandidate{
			instanceType: &cloudprovider.InstanceType{Name: "c9.large.2"},
			zone:         "ap-southeast-3a",
			subnetID:     "subnet-123",
		},
		"HCE OS 2.0",
	)
	if err != nil {
		t.Fatalf("expected node spec creation to succeed, got %v", err)
	}
	if spec.Login == nil {
		t.Fatalf("expected login to be set")
	}
	if spec.Login.SshKey == nil || *spec.Login.SshKey != "cluster-keypair" {
		t.Fatalf("expected ssh key login to be propagated, got %#v", spec.Login.SshKey)
	}
	if spec.Login.UserPassword != nil {
		t.Fatalf("expected password login to be omitted for ssh key mode, got %#v", spec.Login.UserPassword)
	}
}

func TestValidateKubeletForCreateNode(t *testing.T) {
	t.Run("accepts valid kubelet reservations", func(t *testing.T) {
		maxPods := int32(64)
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{
					MaxPods: &maxPods,
					KubeReserved: map[string]string{
						string(corev1.ResourceCPU):              "1500m",
						string(corev1.ResourceMemory):           "1Gi",
						string(corev1.ResourceEphemeralStorage): "2Gi",
						"pid":                                   "1234",
					},
					SystemReserved: map[string]string{
						string(corev1.ResourceCPU):              "250m",
						string(corev1.ResourceMemory):           "512Mi",
						string(corev1.ResourceEphemeralStorage): "1Gi",
						"pid":                                   "4321",
					},
				},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err != nil {
			t.Fatalf("expected kubelet validation to succeed, got %v", err)
		}
	})

	t.Run("rejects maxPods smaller than 16", func(t *testing.T) {
		value := int32(15)
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{MaxPods: &value},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err == nil {
			t.Fatalf("expected undersized maxPods to be rejected")
		}
	})

	t.Run("rejects maxPods larger than 256", func(t *testing.T) {
		value := int32(257)
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{MaxPods: &value},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err == nil {
			t.Fatalf("expected oversized maxPods to be rejected")
		}
	})

	t.Run("rejects kubeReserved memory that is not an exact MiB", func(t *testing.T) {
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{
					KubeReserved: map[string]string{
						string(corev1.ResourceMemory): "1536Ki",
					},
				},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err == nil {
			t.Fatalf("expected non-MiB memory quantity to be rejected")
		}
	})

	t.Run("rejects systemReserved pid that is not an integer", func(t *testing.T) {
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{
					SystemReserved: map[string]string{
						"pid": "1.5",
					},
				},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err == nil {
			t.Fatalf("expected non-integer pid quantity to be rejected")
		}
	})

	t.Run("rejects kubeReserved CPU with surrounding whitespace", func(t *testing.T) {
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{
					KubeReserved: map[string]string{
						string(corev1.ResourceCPU): " 1500m ",
					},
				},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err == nil {
			t.Fatalf("expected CPU quantity with surrounding whitespace to be rejected")
		}
	})

	t.Run("rejects systemReserved pid with surrounding whitespace", func(t *testing.T) {
		nodeClass := &v1alpha1.CCENodeClass{
			Spec: v1alpha1.CCENodeClassSpec{
				Kubelet: &v1alpha1.KubeletConfiguration{
					SystemReserved: map[string]string{
						"pid": " 1234 ",
					},
				},
			},
		}
		if err := ValidateKubeletForCreateNode(nodeClass); err == nil {
			t.Fatalf("expected pid with surrounding whitespace to be rejected")
		}
	})
}

func TestToCCETaints_InjectsKarpenterUnregisteredTaint(t *testing.T) {
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			StartupTaints: []corev1.Taint{
				{
					Key:    "example.com/startup",
					Effect: corev1.TaintEffectNoSchedule,
				},
				karpv1.UnregisteredNoExecuteTaint,
			},
			Taints: []corev1.Taint{
				{
					Key:    "example.com/dedicated",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	got := toCCETaints(nodeClaim)
	if got == nil {
		t.Fatalf("expected taints to be returned")
	}
	if len(*got) != 3 {
		t.Fatalf("expected 3 taints after injection, got %#v", *got)
	}

	keys := map[string]struct{}{}
	for _, taint := range *got {
		keys[taint.Key+"/"+taint.Effect.Value()] = struct{}{}
	}
	if _, ok := keys["example.com/startup/NoSchedule"]; !ok {
		t.Fatalf("expected startup taint to be preserved, got %#v", *got)
	}
	if _, ok := keys["example.com/dedicated/NoSchedule"]; !ok {
		t.Fatalf("expected regular taint to be preserved, got %#v", *got)
	}
	if _, ok := keys[karpv1.UnregisteredTaintKey+"/NoExecute"]; !ok {
		t.Fatalf("expected karpenter unregistered taint to be injected, got %#v", *got)
	}
}

func TestToCCETaints_PrefersCanonicalKarpenterUnregisteredTaint(t *testing.T) {
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			StartupTaints: []corev1.Taint{
				{
					Key:    karpv1.UnregisteredTaintKey,
					Value:  "custom",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
		},
	}

	got := toCCETaints(nodeClaim)
	if got == nil {
		t.Fatalf("expected taints to be returned")
	}
	if len(*got) != 1 {
		t.Fatalf("expected a single deduplicated taint, got %#v", *got)
	}
	if (*got)[0].Key != karpv1.UnregisteredTaintKey {
		t.Fatalf("expected karpenter unregistered taint, got %#v", *got)
	}
	if (*got)[0].Effect.Value() != "NoExecute" {
		t.Fatalf("expected NoExecute effect, got %#v", *got)
	}
	if (*got)[0].Value != nil {
		t.Fatalf("expected canonical unregistered taint to have nil value, got %#v", *got)
	}
}

func TestToCCETaints_ExcludesCCENetworkUnavailableTaint(t *testing.T) {
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			StartupTaints: []corev1.Taint{
				{
					Key:    corev1.TaintNodeNetworkUnavailable,
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
			Taints: []corev1.Taint{
				{
					Key:    "example.com/dedicated",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	got := toCCETaints(nodeClaim)
	if got == nil {
		t.Fatalf("expected taints to be returned")
	}
	if len(*got) != 2 {
		t.Fatalf("expected CCE-managed network-unavailable taint to be filtered, got %#v", *got)
	}
	if hasCCETaint(got, corev1.TaintNodeNetworkUnavailable, "NoSchedule") {
		t.Fatalf("expected network-unavailable taint to be filtered from CreateNode request, got %#v", *got)
	}
	if !hasCCETaint(got, karpv1.UnregisteredTaintKey, "NoExecute") {
		t.Fatalf("expected karpenter unregistered taint to remain, got %#v", *got)
	}
	if !hasCCETaint(got, "example.com/dedicated", "NoSchedule") {
		t.Fatalf("expected regular taint to remain, got %#v", *got)
	}
}

func TestIsInsufficientCapacityError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "KnownCCEFlavorInsufficientErrorCodeIgnoresStatusCode",
			err: &sdkerr.ServiceResponseError{
				StatusCode:   418,
				ErrorCode:    "CCE_CM.0021",
				ErrorMessage: "[as7.xlarge.2|ap-southeast-3a] flavor is " + insufficientInSpecifiedAZMessage,
			},
			want: true,
		},
		{
			name: "FallbackEnglishInsufficientMessageWithoutCodeIgnoresStatusCode",
			err: &sdkerr.ServiceResponseError{
				StatusCode:   418,
				ErrorMessage: "flavor is " + insufficientInSpecifiedAZMessage,
			},
			want: true,
		},
		{
			name: "LowercaseErrorCodeIsNotCanonical",
			err: &sdkerr.ServiceResponseError{
				ErrorCode: "cce_cm.0021",
			},
			want: false,
		},
		{
			name: "NonCanonicalMessageCaseIsNotMatched",
			err: &sdkerr.ServiceResponseError{
				ErrorMessage: "Insufficient capacity",
			},
			want: false,
		},
		{
			name: "QuotaErrorIsNotInventoryShortage",
			err: &sdkerr.ServiceResponseError{
				StatusCode:   400,
				ErrorCode:    "CCE_CM.0099",
				ErrorMessage: "quota exceeded",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInsufficientCapacityError(tt.err); got != tt.want {
				t.Fatalf("expected insufficient=%v, got %v for err=%v", tt.want, got, tt.err)
			}
		})
	}
}

func TestIsUnsupportedNetworkError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "CanonicalErrorCode",
			err: &sdkerr.ServiceResponseError{
				ErrorCode: "CCE.01400025",
			},
			want: true,
		},
		{
			name: "LowercaseErrorCodeIsNotCanonical",
			err: &sdkerr.ServiceResponseError{
				ErrorCode: "cce.01400025",
			},
			want: false,
		},
		{
			name: "CanonicalMessage",
			err: &sdkerr.ServiceResponseError{
				ErrorMessage: "Flavor does not support Eni network is not supported mode",
			},
			want: true,
		},
		{
			name: "NonCanonicalMessageCaseIsNotMatched",
			err: &sdkerr.ServiceResponseError{
				ErrorMessage: "Flavor does not support eni network is not supported mode",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnsupportedNetworkError(tt.err); got != tt.want {
				t.Fatalf("expected unsupported network=%v, got %v for err=%v", tt.want, got, tt.err)
			}
		})
	}
}

type stubCCEAPI struct {
	createNodeResp  *cceMdl.CreateNodeResponse
	createNodeErr   error
	createNodeReqs  []*cceMdl.CreateNodeRequest
	createNodeResps []*cceMdl.CreateNodeResponse
	createNodeErrs  []error
	showNodeResp    *cceMdl.ShowNodeResponse
	listNodesResp   *cceMdl.ListNodesResponse
}

func (s *stubCCEAPI) ShowCluster(*cceMdl.ShowClusterRequest) (*cceMdl.ShowClusterResponse, error) {
	return nil, nil
}

func (s *stubCCEAPI) CreateNode(req *cceMdl.CreateNodeRequest) (*cceMdl.CreateNodeResponse, error) {
	s.createNodeReqs = append(s.createNodeReqs, req)
	if len(s.createNodeResps) > 0 || len(s.createNodeErrs) > 0 {
		var resp *cceMdl.CreateNodeResponse
		var err error
		if len(s.createNodeResps) > 0 {
			resp = s.createNodeResps[0]
			s.createNodeResps = s.createNodeResps[1:]
		}
		if len(s.createNodeErrs) > 0 {
			err = s.createNodeErrs[0]
			s.createNodeErrs = s.createNodeErrs[1:]
		}
		return resp, err
	}
	return s.createNodeResp, s.createNodeErr
}

func (s *stubCCEAPI) DeleteNode(*cceMdl.DeleteNodeRequest) (*cceMdl.DeleteNodeResponse, error) {
	return nil, nil
}

func (s *stubCCEAPI) ListNodes(*cceMdl.ListNodesRequest) (*cceMdl.ListNodesResponse, error) {
	return s.listNodesResp, nil
}

func (s *stubCCEAPI) ShowNode(*cceMdl.ShowNodeRequest) (*cceMdl.ShowNodeResponse, error) {
	return s.showNodeResp, nil
}

func (s *stubCCEAPI) ShowJob(*cceMdl.ShowJobRequest) (*cceMdl.ShowJobResponse, error) {
	return nil, nil
}

type stubSubnetProvider struct {
	selectedSubnet *subnet.Subnet
}

func (s *stubSubnetProvider) LivenessProbe(*http.Request) error {
	return nil
}

func (s *stubSubnetProvider) List(context.Context, *v1alpha1.CCENodeClass) ([]vpcMdl.Subnet, error) {
	return nil, nil
}

func (s *stubSubnetProvider) SelectForLaunch(context.Context, *v1alpha1.CCENodeClass, []*cloudprovider.InstanceType, string) (*subnet.Subnet, error) {
	return s.selectedSubnet, nil
}

func (s *stubSubnetProvider) ReleaseInflightIPs(*subnet.Subnet) {
}

var (
	_ sdk.CCEAPI      = (*stubCCEAPI)(nil)
	_ subnet.Provider = (*stubSubnetProvider)(nil)
)

func TestCreate_AllowsEmptyServerIDInCreateNodeResponse(t *testing.T) {
	cceapi := &stubCCEAPI{
		createNodeResp: &cceMdl.CreateNodeResponse{
			Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-123")},
			Status:   &cceMdl.NodeStatus{},
		},
	}
	provider := &DefaultProvider{
		clusterID:      "cluster-id",
		cceapi:         cceapi,
		subnetProvider: &stubSubnetProvider{selectedSubnet: &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100}},
	}
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "Huawei Cloud EulerOS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{VolumeSize: 120, VolumeType: "SAS"},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{Password: "ciphertext"},
			},
		},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-a"}},
		},
	}
	nodeClaim := &karpv1.NodeClaim{}
	instanceTypes := []*cloudprovider.InstanceType{{
		Name: "c9.large.2",
		Offerings: cloudprovider.Offerings{
			{
				Available: true,
				Requirements: scheduling.NewRequirements(
					scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
					scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "zone-a"),
				),
			},
		},
	}}

	instance, err := provider.Create(context.Background(), nodeClass, nodeClaim, instanceTypes)
	if err != nil {
		t.Fatalf("expected create to succeed without server id, got %v", err)
	}
	if instance.NodeID != "node-123" {
		t.Fatalf("expected node id %q, got %q", "node-123", instance.NodeID)
	}
	if instance.ServerID != "" {
		t.Fatalf("expected empty server id, got %q", instance.ServerID)
	}
	if len(cceapi.createNodeReqs) != 1 {
		t.Fatalf("expected exactly one CreateNode call, got %d", len(cceapi.createNodeReqs))
	}
	if !hasCCETaint(cceapi.createNodeReqs[0].Body.Spec.Taints, karpv1.UnregisteredTaintKey, "NoExecute") {
		t.Fatalf("expected CreateNode request to include karpenter unregistered taint, got %#v", cceapi.createNodeReqs[0].Body.Spec.Taints)
	}
}

func TestCreate_PrefersCheaperCandidate(t *testing.T) {
	cceapi := &stubCCEAPI{
		createNodeResp: &cceMdl.CreateNodeResponse{
			Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-123")},
			Status:   &cceMdl.NodeStatus{},
		},
	}
	provider := &DefaultProvider{
		clusterID:      "cluster-id",
		cceapi:         cceapi,
		subnetProvider: &stubSubnetProvider{selectedSubnet: &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100}},
	}
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "Huawei Cloud EulerOS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{VolumeSize: 120, VolumeType: "SAS"},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{Password: "ciphertext"},
			},
		},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-a"}},
		},
	}
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"ac7.2xlarge.1", "x1.6u.10g"},
				},
			},
		},
	}
	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "ac7.2xlarge.1",
			Offerings: cloudprovider.Offerings{
				{
					Available: true,
					Price:     1.8402,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
						scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "ap-southeast-3a"),
					),
				},
			},
		},
		{
			Name: "x1.6u.10g",
			Offerings: cloudprovider.Offerings{
				{
					Available: true,
					Price:     1.4951,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
						scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "ap-southeast-3a"),
					),
				},
			},
		},
	}

	instance, err := provider.Create(context.Background(), nodeClass, nodeClaim, instanceTypes)
	if err != nil {
		t.Fatalf("expected create to succeed, got %v", err)
	}
	if instance.Flavor != "x1.6u.10g" {
		t.Fatalf("expected cheaper flavor to be launched, got %q", instance.Flavor)
	}
	if len(cceapi.createNodeReqs) != 1 {
		t.Fatalf("expected exactly one CreateNode call, got %d", len(cceapi.createNodeReqs))
	}
	if got := cceapi.createNodeReqs[0].Body.Spec.Flavor; got != "x1.6u.10g" {
		t.Fatalf("expected CreateNode to request cheaper flavor, got %q", got)
	}
	if !hasCCETaint(cceapi.createNodeReqs[0].Body.Spec.Taints, karpv1.UnregisteredTaintKey, "NoExecute") {
		t.Fatalf("expected CreateNode request to include karpenter unregistered taint, got %#v", cceapi.createNodeReqs[0].Body.Spec.Taints)
	}
}

func TestCreate_FallsBackWhenCheapestFlavorDoesNotSupportENINetwork(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(time.Minute, time.Minute)
	cceapi := &stubCCEAPI{
		createNodeResps: []*cceMdl.CreateNodeResponse{
			nil,
			{
				Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-456")},
				Status:   &cceMdl.NodeStatus{},
			},
		},
		createNodeErrs: []error{
			&sdkerr.ServiceResponseError{
				StatusCode:   400,
				ErrorCode:    "CCE.01400025",
				ErrorMessage: "Subeni quota is not enough of VM's flavor, Flavor [t7.xlarge.2] 's subeni quota is 0, Eni network is not supported ",
			},
			nil,
		},
	}
	provider := &DefaultProvider{
		clusterID:                 "cluster-id",
		cceapi:                    cceapi,
		offeringAvailabilityCache: availabilityCache,
		subnetProvider:            &stubSubnetProvider{selectedSubnet: &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100}},
	}
	nodeClass := &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "Huawei Cloud EulerOS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{VolumeSize: 120, VolumeType: "SAS"},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{Password: "ciphertext"},
			},
		},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-a"}},
		},
	}
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			Requirements: []karpv1.NodeSelectorRequirementWithMinValues{
				{
					Key:      corev1.LabelInstanceTypeStable,
					Operator: corev1.NodeSelectorOpIn,
					Values:   []string{"t7.xlarge.2", "t6.xlarge.2"},
				},
			},
		},
	}
	instanceTypes := []*cloudprovider.InstanceType{
		{
			Name: "t7.xlarge.2",
			Offerings: cloudprovider.Offerings{
				{
					Available: true,
					Price:     0.2033,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
						scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "ap-southeast-3a"),
					),
				},
			},
		},
		{
			Name: "t6.xlarge.2",
			Offerings: cloudprovider.Offerings{
				{
					Available: true,
					Price:     0.6876,
					Requirements: scheduling.NewRequirements(
						scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
						scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "ap-southeast-3a"),
					),
				},
			},
		},
	}

	instance, err := provider.Create(context.Background(), nodeClass, nodeClaim, instanceTypes)
	if err != nil {
		t.Fatalf("expected create to succeed after falling back to next candidate, got %v", err)
	}
	if instance.Flavor != "t6.xlarge.2" {
		t.Fatalf("expected fallback flavor %q, got %q", "t6.xlarge.2", instance.Flavor)
	}
	if len(cceapi.createNodeReqs) != 2 {
		t.Fatalf("expected two CreateNode calls, got %d", len(cceapi.createNodeReqs))
	}
	if got := cceapi.createNodeReqs[0].Body.Spec.Flavor; got != "t7.xlarge.2" {
		t.Fatalf("expected first CreateNode to request cheapest flavor, got %q", got)
	}
	if got := cceapi.createNodeReqs[1].Body.Spec.Flavor; got != "t6.xlarge.2" {
		t.Fatalf("expected second CreateNode to request fallback flavor, got %q", got)
	}
	if !availabilityCache.IsUnavailable(karpv1.CapacityTypeOnDemand, "t7.xlarge.2", "ap-southeast-3a") {
		t.Fatalf("expected ENI-unsupported offering to be marked unavailable")
	}
}

func TestCreate_FallsBackWhenAvailabilityCacheIsNil(t *testing.T) {
	cceapi := &stubCCEAPI{
		createNodeResps: []*cceMdl.CreateNodeResponse{
			nil,
			{
				Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-789")},
				Status:   &cceMdl.NodeStatus{},
			},
		},
		createNodeErrs: []error{
			&sdkerr.ServiceResponseError{
				StatusCode:   409,
				ErrorCode:    "CCE_CM.0021",
				ErrorMessage: "[x1e.12u.96g|ap-southeast-3a] flavor is insufficient in specified az",
			},
			nil,
		},
	}
	provider := &DefaultProvider{
		clusterID:      "cluster-id",
		cceapi:         cceapi,
		subnetProvider: newStubSubnetProvider(),
	}

	instance, err := provider.Create(context.Background(), newTestNodeClass(), &karpv1.NodeClaim{}, []*cloudprovider.InstanceType{
		newOnDemandInstanceType("x1e.12u.96g", 1.0, "ap-southeast-3a"),
		newOnDemandInstanceType("c7.4xlarge.2", 2.0, "ap-southeast-3a"),
	})
	if err != nil {
		t.Fatalf("expected create to succeed after fallback without an availability cache, got %v", err)
	}
	if instance.Flavor != "c7.4xlarge.2" {
		t.Fatalf("expected fallback flavor %q, got %q", "c7.4xlarge.2", instance.Flavor)
	}
	if len(cceapi.createNodeReqs) != 2 {
		t.Fatalf("expected two CreateNode calls, got %d", len(cceapi.createNodeReqs))
	}
}

func TestCreate_MarksUnavailableOfferingAndFallsBackOnInsufficientCapacity(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(time.Minute, time.Minute)
	cceapi := &stubCCEAPI{
		createNodeResps: []*cceMdl.CreateNodeResponse{
			nil,
			{
				Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-789")},
				Status:   &cceMdl.NodeStatus{},
			},
		},
		createNodeErrs: []error{
			&sdkerr.ServiceResponseError{
				StatusCode:   409,
				ErrorCode:    "CCE_CM.0021",
				ErrorMessage: "[x1e.12u.96g|ap-southeast-3a] flavor is insufficient in specified az",
			},
			nil,
		},
	}
	provider := &DefaultProvider{
		clusterID:                 "cluster-id",
		cceapi:                    cceapi,
		subnetProvider:            newStubSubnetProvider(),
		offeringAvailabilityCache: availabilityCache,
	}

	instance, err := provider.Create(context.Background(), newTestNodeClass(), &karpv1.NodeClaim{}, []*cloudprovider.InstanceType{
		newOnDemandInstanceType("x1e.12u.96g", 1.0, "ap-southeast-3a"),
		newOnDemandInstanceType("c7.4xlarge.2", 2.0, "ap-southeast-3a"),
	})
	if err != nil {
		t.Fatalf("expected create to succeed after fallback, got %v", err)
	}
	if instance.Flavor != "c7.4xlarge.2" {
		t.Fatalf("expected fallback flavor %q, got %q", "c7.4xlarge.2", instance.Flavor)
	}
	if len(cceapi.createNodeReqs) != 2 {
		t.Fatalf("expected two CreateNode calls, got %d", len(cceapi.createNodeReqs))
	}
	if !availabilityCache.IsUnavailable(karpv1.CapacityTypeOnDemand, "x1e.12u.96g", "ap-southeast-3a") {
		t.Fatalf("expected insufficient offering to be marked unavailable")
	}
}

func TestCreate_SkipsUnavailableOfferingsOnSubsequentCalls(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(time.Minute, time.Minute)
	availabilityCache.MarkUnavailable(karpv1.CapacityTypeOnDemand, "x1e.12u.96g", "ap-southeast-3a")

	cceapi := &stubCCEAPI{
		createNodeResp: &cceMdl.CreateNodeResponse{
			Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-999")},
			Status:   &cceMdl.NodeStatus{},
		},
	}
	provider := &DefaultProvider{
		clusterID:                 "cluster-id",
		cceapi:                    cceapi,
		subnetProvider:            newStubSubnetProvider(),
		offeringAvailabilityCache: availabilityCache,
	}

	instance, err := provider.Create(context.Background(), newTestNodeClass(), &karpv1.NodeClaim{}, []*cloudprovider.InstanceType{
		newOnDemandInstanceType("x1e.12u.96g", 1.0, "ap-southeast-3a"),
		newOnDemandInstanceType("c7.4xlarge.2", 2.0, "ap-southeast-3a"),
		newOnDemandInstanceType("m7.4xlarge.2", 3.0, "ap-southeast-3a"),
	})
	if err != nil {
		t.Fatalf("expected create to succeed after skipping cached offering, got %v", err)
	}
	if instance.Flavor != "c7.4xlarge.2" {
		t.Fatalf("expected next cheapest available flavor %q, got %q", "c7.4xlarge.2", instance.Flavor)
	}
	if len(cceapi.createNodeReqs) != 1 {
		t.Fatalf("expected one CreateNode call after skipping cached offering, got %d", len(cceapi.createNodeReqs))
	}
	if got := cceapi.createNodeReqs[0].Body.Spec.Flavor; got != "c7.4xlarge.2" {
		t.Fatalf("expected CreateNode to skip cached cheaper flavor and request %q, got %q", "c7.4xlarge.2", got)
	}
}

func TestCreate_ReturnsInsufficientCapacityWhenAllCompatibleOfferingsTemporarilyUnavailable(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(time.Minute, time.Minute)
	availabilityCache.MarkUnavailable(karpv1.CapacityTypeOnDemand, "x1e.12u.96g", "ap-southeast-3a")
	availabilityCache.MarkUnavailable(karpv1.CapacityTypeOnDemand, "c7.4xlarge.2", "ap-southeast-3a")

	cceapi := &stubCCEAPI{}
	provider := &DefaultProvider{
		clusterID:                 "cluster-id",
		cceapi:                    cceapi,
		subnetProvider:            newStubSubnetProvider(),
		offeringAvailabilityCache: availabilityCache,
	}

	_, err := provider.Create(context.Background(), newTestNodeClass(), &karpv1.NodeClaim{}, []*cloudprovider.InstanceType{
		newOnDemandInstanceType("x1e.12u.96g", 1.0, "ap-southeast-3a"),
		newOnDemandInstanceType("c7.4xlarge.2", 2.0, "ap-southeast-3a"),
	})
	if !cloudprovider.IsInsufficientCapacityError(err) {
		t.Fatalf("expected insufficient capacity error, got %v", err)
	}
	if len(cceapi.createNodeReqs) != 0 {
		t.Fatalf("expected no CreateNode calls when all compatible offerings are temporarily unavailable, got %d", len(cceapi.createNodeReqs))
	}
}

func TestCreate_UnavailableOfferingCacheIsZoneScoped(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(time.Minute, time.Minute)
	availabilityCache.MarkUnavailable(karpv1.CapacityTypeOnDemand, "x1e.12u.96g", "ap-southeast-3a")

	cceapi := &stubCCEAPI{
		createNodeResp: &cceMdl.CreateNodeResponse{
			Metadata: &cceMdl.NodeMetadata{Uid: lo.ToPtr("node-zone-b")},
			Status:   &cceMdl.NodeStatus{},
		},
	}
	provider := &DefaultProvider{
		clusterID:                 "cluster-id",
		cceapi:                    cceapi,
		subnetProvider:            &stubSubnetProvider{selectedSubnet: &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100}},
		offeringAvailabilityCache: availabilityCache,
	}

	instance, err := provider.Create(context.Background(), newTestNodeClass(), &karpv1.NodeClaim{}, []*cloudprovider.InstanceType{
		newOnDemandInstanceType("x1e.12u.96g", 1.0, "ap-southeast-3a", "ap-southeast-3b"),
	})
	if err != nil {
		t.Fatalf("expected create to succeed using unaffected zone, got %v", err)
	}
	if instance.Zone != "ap-southeast-3b" {
		t.Fatalf("expected zone-scoped unavailability to allow zone-b, got %q", instance.Zone)
	}
	if len(cceapi.createNodeReqs) != 1 {
		t.Fatalf("expected one CreateNode call, got %d", len(cceapi.createNodeReqs))
	}
	if got := cceapi.createNodeReqs[0].Body.Spec.Az; got != "ap-southeast-3b" {
		t.Fatalf("expected CreateNode to target unaffected zone %q, got %q", "ap-southeast-3b", got)
	}
	if got := lo.FromPtr(cceapi.createNodeReqs[0].Body.Spec.NodeNicSpec.PrimaryNic.SubnetId); got != "subnet-a" {
		t.Fatalf("expected CreateNode in zone-b to reuse subnet-a, got %q", got)
	}
}

func newTestNodeClass() *v1alpha1.CCENodeClass {
	return &v1alpha1.CCENodeClass{
		Spec: v1alpha1.CCENodeClassSpec{
			IMSSelector: v1alpha1.IMSSelector{IMSFamily: "Huawei Cloud EulerOS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{VolumeSize: 120, VolumeType: "SAS"},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{Password: "ciphertext"},
			},
		},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-a"}},
		},
	}
}

func newStubSubnetProvider() *stubSubnetProvider {
	return &stubSubnetProvider{
		selectedSubnet: &subnet.Subnet{ID: "subnet-a", AvailableIPAddressCount: 100},
	}
}

func newOnDemandInstanceType(name string, price float64, zones ...string) *cloudprovider.InstanceType {
	offerings := make(cloudprovider.Offerings, 0, len(zones))
	for _, zone := range zones {
		offerings = append(offerings, &cloudprovider.Offering{
			Available: true,
			Price:     price,
			Requirements: scheduling.NewRequirements(
				scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
				scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
			),
		})
	}
	return &cloudprovider.InstanceType{Name: name, Offerings: offerings}
}

func hasCCETaint(taints *[]cceMdl.Taint, key, effect string) bool {
	if taints == nil {
		return false
	}
	for _, taint := range *taints {
		if taint.Key == key && taint.Effect.Value() == effect {
			return true
		}
	}
	return false
}
