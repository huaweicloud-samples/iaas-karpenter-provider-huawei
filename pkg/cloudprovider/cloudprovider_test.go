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

func TestResolvedNodeClaimLabels_IncludesRestrictedWellKnownLabels(t *testing.T) {
	instanceType := &karpcloudprovider.InstanceType{
		Name: "c9.large.2",
		Requirements: karpscheduling.NewRequirements(
			karpscheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, "amd64"),
			karpscheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
			karpscheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, "ap-southeast-3"),
			karpscheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "ap-southeast-3a", "ap-southeast-3b"),
		),
	}
	createdInstance := &instanceprovider.Instance{
		Flavor: "c9.large.2",
		Zone:   "ap-southeast-3a",
	}

	labels := resolvedNodeClaimLabels(instanceType, createdInstance)

	if got := labels[corev1.LabelInstanceTypeStable]; got != "c9.large.2" {
		t.Fatalf("expected instance-type label %q, got %q", "c9.large.2", got)
	}
	if got := labels[corev1.LabelTopologyZone]; got != "ap-southeast-3a" {
		t.Fatalf("expected zone label %q, got %q", "ap-southeast-3a", got)
	}
	if got := labels[karpv1.CapacityTypeLabelKey]; got != karpv1.CapacityTypeOnDemand {
		t.Fatalf("expected capacity-type label %q, got %q", karpv1.CapacityTypeOnDemand, got)
	}
	if got := labels[corev1.LabelArchStable]; got != "amd64" {
		t.Fatalf("expected arch label %q, got %q", "amd64", got)
	}
	if got := labels[corev1.LabelOSStable]; got != string(corev1.Linux) {
		t.Fatalf("expected os label %q, got %q", corev1.Linux, got)
	}
	if got := labels[corev1.LabelTopologyRegion]; got != "ap-southeast-3" {
		t.Fatalf("expected region label %q, got %q", "ap-southeast-3", got)
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

func TestCreate_AnnotatesReturnedNodeClaimWithCCENodeClassHash(t *testing.T) {
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
				Name: "c9.large.2",
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
				Overhead: &karpcloudprovider.InstanceTypeOverhead{},
				Requirements: karpscheduling.NewRequirements(
					karpscheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, "zone-a"),
				),
			}},
		},
		instanceProvider: &stubCloudProviderInstanceProvider{
			instance: &instanceprovider.Instance{
				NodeID:   "node-123",
				ServerID: "server-123",
				Flavor:   "c9.large.2",
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
		t.Fatalf("expected create to succeed, got %v", err)
	}
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
