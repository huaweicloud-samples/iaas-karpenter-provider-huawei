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

package cloudprovider

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcloudprovider "sigs.k8s.io/karpenter/pkg/cloudprovider"
	karpscheduling "sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
	sdk "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/huawei"
	instanceprovider "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instance"
	instancetypeprovider "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instancetype"
)

func TestCreate_ReturnsResolvedHuaweiLabels(t *testing.T) {
	testCases := []struct {
		name         string
		flavor       string
		requirements karpscheduling.Requirements
		want         map[string]string
		absent       []string
	}{
		{
			name:   "complete labels",
			flavor: "c9.large.2",
			requirements: karpscheduling.NewRequirements(
				karpscheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
				karpscheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
				karpscheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "ap-southeast-3"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpIn, "c"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, "c9"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpIn, "9"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceCPU, corev1.NodeSelectorOpIn, "2"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceMemory, corev1.NodeSelectorOpIn, "4096"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceSize, corev1.NodeSelectorOpIn, "large"),
			),
			want: map[string]string{
				corev1.LabelArchStable:           "amd64",
				corev1.LabelOSStable:             string(corev1.Linux),
				corev1.LabelTopologyRegion:       "ap-southeast-3",
				v1alpha1.LabelInstanceCategory:   "c",
				v1alpha1.LabelInstanceFamily:     "c9",
				v1alpha1.LabelInstanceGeneration: "9",
				v1alpha1.LabelInstanceCPU:        "2",
				v1alpha1.LabelInstanceMemory:     "4096",
				v1alpha1.LabelInstanceSize:       "large",
				corev1.LabelInstanceTypeStable:   "c9.large.2",
				corev1.LabelTopologyZone:         "zone-a",
				karpv1.CapacityTypeLabelKey:      karpv1.CapacityTypeOnDemand,
			},
		},
		{
			name:   "partial labels",
			flavor: "zc12e.01xlarge.2",
			requirements: karpscheduling.NewRequirements(
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, "zc12e"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpIn, "12"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceCPU, corev1.NodeSelectorOpIn, "2"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceMemory, corev1.NodeSelectorOpIn, "4096"),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpDoesNotExist),
				karpscheduling.NewRequirement(v1alpha1.LabelInstanceSize, corev1.NodeSelectorOpDoesNotExist),
			),
			want: map[string]string{
				v1alpha1.LabelInstanceFamily:     "zc12e",
				v1alpha1.LabelInstanceGeneration: "12",
				v1alpha1.LabelInstanceCPU:        "2",
				v1alpha1.LabelInstanceMemory:     "4096",
				corev1.LabelInstanceTypeStable:   "zc12e.01xlarge.2",
				corev1.LabelTopologyZone:         "zone-a",
				karpv1.CapacityTypeLabelKey:      karpv1.CapacityTypeOnDemand,
			},
			absent: []string{v1alpha1.LabelInstanceCategory, v1alpha1.LabelInstanceSize},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			created, _ := launchTestNodeClaim(t, tc.flavor, tc.requirements)
			for key, want := range tc.want {
				if got := created.Labels[key]; got != want {
					t.Errorf("expected label %q=%q, got %q", key, want, got)
				}
			}
			for _, key := range tc.absent {
				if value, ok := created.Labels[key]; ok {
					t.Errorf("expected label %q to be absent, got %q", key, value)
				}
			}
		})
	}
}

type stubCloudProviderInstanceTypeProvider struct {
	instanceTypes []*karpcloudprovider.InstanceType
	err           error
}

func (s *stubCloudProviderInstanceTypeProvider) Get(context.Context, instancetypeprovider.NodeClass, sdk.InstanceType) (*karpcloudprovider.InstanceType, error) {
	return nil, nil
}

func (s *stubCloudProviderInstanceTypeProvider) List(context.Context, instancetypeprovider.NodeClass) ([]*karpcloudprovider.InstanceType, error) {
	return s.instanceTypes, s.err
}

type stubCloudProviderInstanceProvider struct {
	instance    *instanceprovider.Instance
	err         error
	createCalls int
}

func (s *stubCloudProviderInstanceProvider) Create(context.Context, *v1alpha1.CCENodeClass, *karpv1.NodeClaim, []*karpcloudprovider.InstanceType) (*instanceprovider.Instance, error) {
	s.createCalls++
	return s.instance, s.err
}

func (s *stubCloudProviderInstanceProvider) Get(context.Context, string) (*instanceprovider.Instance, error) {
	return s.instance, s.err
}

func (s *stubCloudProviderInstanceProvider) List(context.Context) ([]*instanceprovider.Instance, error) {
	if s.instance == nil {
		return nil, s.err
	}
	return []*instanceprovider.Instance{s.instance}, s.err
}

func (s *stubCloudProviderInstanceProvider) Delete(context.Context, string) error {
	return s.err
}

func TestAreStaticFieldsDrifted_ReturnsNodeClassDriftWhenHashesDiffer(t *testing.T) {
	provider := &CloudProvider{}
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1alpha1.AnnotationCCENodeClassHash:        "hash-a",
				v1alpha1.AnnotationCCENodeClassHashVersion: "v1",
			},
		},
	}
	nodeClass := &v1alpha1.CCENodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				v1alpha1.AnnotationCCENodeClassHash:        "hash-b",
				v1alpha1.AnnotationCCENodeClassHashVersion: "v1",
			},
		},
	}

	if got := provider.areStaticFieldsDrifted(nodeClaim, nodeClass); got != NodeClassDrift {
		t.Fatalf("expected drift reason %q, got %q", NodeClassDrift, got)
	}
}

func launchTestNodeClaim(t *testing.T, flavor string, requirements karpscheduling.Requirements) (*karpv1.NodeClaim, *v1alpha1.CCENodeClass) {
	t.Helper()
	nodeClass := &v1alpha1.CCENodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.CCENodeClassSpec{
			SubnetSelectorTerms: []v1alpha1.SubnetSelectorTerm{{ID: "123e4567-e89b-12d3-a456-426614174000"}},
			IMSSelector:         v1alpha1.IMSSelector{IMSFamily: "HCE OS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{VolumeSize: 120, VolumeType: "SAS"},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{Password: "ciphertext"},
			},
		},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-123"}},
		},
	}
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionTypeSubnetsReady)

	kubeClient := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(nodeClass).Build()
	provider := &CloudProvider{
		kubeClient: kubeClient,
		instanceTypeProvider: &stubCloudProviderInstanceTypeProvider{
			instanceTypes: []*karpcloudprovider.InstanceType{{
				Name: flavor,
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
				Overhead:     &karpcloudprovider.InstanceTypeOverhead{},
				Requirements: requirements,
			}},
		},
		instanceProvider: &stubCloudProviderInstanceProvider{
			instance: &instanceprovider.Instance{
				NodeID:   "node-123",
				ServerID: "server-123",
				Flavor:   flavor,
				Zone:     "zone-a",
			},
		},
	}
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.k8s.huawei",
				Kind:  "CCENodeClass",
				Name:  "default",
			},
		},
	}

	created, err := provider.Create(context.Background(), nodeClaim)
	if err != nil {
		t.Fatalf("creating test NodeClaim: %v", err)
	}
	return created, nodeClass
}

func TestCreate_AnnotatesReturnedNodeClaimWithCCENodeClassHash(t *testing.T) {
	created, nodeClass := launchTestNodeClaim(t, "c9.large.2", karpscheduling.NewRequirements(
		karpscheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "zone-a"),
	))

	if got := created.Annotations[v1alpha1.AnnotationNodeID]; got != "node-123" {
		t.Fatalf("expected node id annotation %q, got %q", "node-123", got)
	}
	if got := created.Annotations[v1alpha1.AnnotationInstanceID]; got != "server-123" {
		t.Fatalf("expected instance id annotation %q, got %q", "server-123", got)
	}
	if got := created.Annotations[v1alpha1.AnnotationCCENodeClassHash]; got != nodeClass.Hash() {
		t.Fatalf("expected ccenodeclass hash annotation %q, got %q", nodeClass.Hash(), got)
	}
	if got := created.Annotations[v1alpha1.AnnotationCCENodeClassHashVersion]; got != v1alpha1.CCENodeClassHashVersion {
		t.Fatalf("expected ccenodeclass hash version %q, got %q", v1alpha1.CCENodeClassHashVersion, got)
	}
}

func TestGetAndList_DoNotBackfillResolvedHuaweiLabels(t *testing.T) {
	provider := &CloudProvider{
		instanceProvider: &stubCloudProviderInstanceProvider{
			instance: &instanceprovider.Instance{NodeID: "node-123"},
		},
	}

	got, err := provider.Get(context.Background(), "node-123")
	if err != nil {
		t.Fatalf("getting NodeClaim: %v", err)
	}
	if len(got.Labels) != 0 {
		t.Fatalf("expected Get not to backfill labels, got %v", got.Labels)
	}

	listed, err := provider.List(context.Background())
	if err != nil {
		t.Fatalf("listing NodeClaims: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one NodeClaim, got %d", len(listed))
	}
	if len(listed[0].Labels) != 0 {
		t.Fatalf("expected List not to backfill labels, got %v", listed[0].Labels)
	}
}

func TestIsDrifted_DoesNotRequireResolvedHuaweiLabels(t *testing.T) {
	nodeClass := &v1alpha1.CCENodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-123"}},
		},
	}
	nodePool := &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{
						Group: "karpenter.k8s.huawei",
						Kind:  "CCENodeClass",
						Name:  nodeClass.Name,
					},
				},
			},
		},
	}
	provider := &CloudProvider{
		kubeClient: fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(nodeClass, nodePool).Build(),
		instanceProvider: &stubCloudProviderInstanceProvider{
			instance: &instanceprovider.Instance{SubnetID: "subnet-123"},
		},
	}
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{karpv1.NodePoolLabelKey: nodePool.Name},
		},
		Status: karpv1.NodeClaimStatus{ProviderID: "node-123"},
	}

	driftReason, err := provider.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatalf("checking drift: %v", err)
	}
	if driftReason != "" {
		t.Fatalf("expected missing resolved Huawei labels not to cause drift, got %q", driftReason)
	}
	if len(nodeClaim.Labels) != 1 {
		t.Fatalf("expected drift check not to backfill labels, got %v", nodeClaim.Labels)
	}
}

func TestCreate_RejectsNodeClassWithUndersizedDataVolumeBeforeCallingCloudAPI(t *testing.T) {
	nodeClass := &v1alpha1.CCENodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.CCENodeClassSpec{
			SubnetSelectorTerms: []v1alpha1.SubnetSelectorTerm{{ID: "123e4567-e89b-12d3-a456-426614174000"}},
			IMSSelector:         v1alpha1.IMSSelector{IMSFamily: "Huawei Cloud EulerOS 2.0"},
			BlockDeviceMappings: v1alpha1.BlockDeviceMappings{
				Root: v1alpha1.BlockDevice{VolumeSize: 40, VolumeType: "SSD"},
				Users: []v1alpha1.BlockDevice{{
					VolumeSize: 9,
					VolumeType: "SAS",
				}},
			},
			Login: v1alpha1.Login{
				UserPassword: &v1alpha1.UserPassword{Password: "ciphertext"},
			},
		},
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-123"}},
		},
	}
	nodeClass.StatusConditions().SetTrue(v1alpha1.ConditionTypeSubnetsReady)

	kubeClient := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(nodeClass).Build()
	instanceProvider := &stubCloudProviderInstanceProvider{}
	provider := &CloudProvider{
		kubeClient: kubeClient,
		instanceTypeProvider: &stubCloudProviderInstanceTypeProvider{
			instanceTypes: []*karpcloudprovider.InstanceType{{
				Name: "c9.large.2",
				Requirements: karpscheduling.NewRequirements(
					karpscheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "zone-a"),
				),
			}},
		},
		instanceProvider: instanceProvider,
	}
	nodeClaim := &karpv1.NodeClaim{
		Spec: karpv1.NodeClaimSpec{
			NodeClassRef: &karpv1.NodeClassReference{
				Group: "karpenter.k8s.huawei",
				Kind:  "CCENodeClass",
				Name:  "default",
			},
		},
	}

	_, err := provider.Create(context.Background(), nodeClaim)
	if err == nil {
		t.Fatalf("expected create to fail for undersized data volume")
	}
	if instanceProvider.createCalls != 0 {
		t.Fatalf("expected instance provider not to be called, got %d call(s)", instanceProvider.createCalls)
	}
	if got, want := err.Error(), "nodeClass.spec.blockDeviceMappings.users[0].volumeSize must be at least 10Gi"; !strings.Contains(got, want) {
		t.Fatalf("expected error to contain %q, got %q", want, got)
	}
}

func TestIsSubnetDrifted_ReturnsExpectedDriftReason(t *testing.T) {
	provider := &CloudProvider{}
	nodeClass := &v1alpha1.CCENodeClass{
		Status: v1alpha1.CCENodeClassStatus{
			Subnets: []v1alpha1.Subnet{{ID: "subnet-123"}},
		},
	}

	testCases := []struct {
		name     string
		subnetID string
		want     karpcloudprovider.DriftReason
	}{
		{
			name:     "matching subnet",
			subnetID: "subnet-123",
			want:     "",
		},
		{
			name:     "missing subnet",
			subnetID: "subnet-456",
			want:     SubnetDrift,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := provider.isSubnetDrifted(&instanceprovider.Instance{SubnetID: tc.subnetID}, nodeClass)
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected drift reason %q, got %q", tc.want, got)
			}
		})
	}
}
