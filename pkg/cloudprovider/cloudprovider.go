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
	"fmt"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instance"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instancetype"
)

var _ cloudprovider.CloudProvider = (*CloudProvider)(nil)

var resolvedNodeClaimLabelKeys = []string{
	corev1.LabelArchStable,
	corev1.LabelOSStable,
	corev1.LabelTopologyRegion,
}

// CloudProvider implements Karpenter's CloudProvider interface for Huawei Cloud.
type CloudProvider struct {
	kubeClient           client.Client
	recorder             events.Recorder
	instanceTypeProvider instancetype.Provider
	instanceProvider     instance.Provider
}

// New creates a Huawei CloudProvider implementation.
func New(kubeClient client.Client, recorder events.Recorder, instanceTypeProvider instancetype.Provider, instanceProvider instance.Provider) *CloudProvider {
	return &CloudProvider{
		kubeClient:           kubeClient,
		recorder:             recorder,
		instanceTypeProvider: instanceTypeProvider,
		instanceProvider:     instanceProvider,
	}
}

// Create is called by Karpenter to provision capacity for a NodeClaim.
func (c *CloudProvider) Create(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*karpv1.NodeClaim, error) {
	nodeClass, err := c.resolveNodeClassFromNodeClaim(ctx, nodeClaim)
	if err != nil {
		if errors.IsNotFound(err) {
			c.recorder.Publish(events.Event{
				InvolvedObject: nodeClaim,
				Type:           corev1.EventTypeWarning,
				Reason:         "NodeClassNotFound",
				Message:        "Failed To Resolve NodeClass",
				DedupeValues:   []string{string(nodeClaim.UID)},
			})
			return nil, cloudprovider.NewNodeClassNotReadyError(err)
		}
		return nil, fmt.Errorf("resolving nodeclass, %w", err)
	}
	subnetsReady := nodeClass.StatusConditions().Get(v1alpha1.ConditionTypeSubnetsReady)
	if !subnetsReady.IsTrue() || len(nodeClass.Status.Subnets) == 0 {
		return nil, cloudprovider.NewNodeClassNotReadyError(fmt.Errorf("nodeclass subnets not ready: %s", subnetsReady.Message))
	}
	osAlias, err := nodeClass.Spec.ResolveIMSForCreateNode()
	if err != nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("resolving nodeclass imsSelector, %w", err), "InvalidNodeClass", "Invalid NodeClass imsSelector")
	}
	if err := nodeClass.Spec.ValidateForCreateNode(); err != nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("validating nodeclass, %w", err), "InvalidNodeClass", "Invalid NodeClass spec")
	}
	if err := instance.ValidateKubeletForCreateNode(nodeClass); err != nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("validating nodeclass kubelet, %w", err), "InvalidNodeClass", "Invalid NodeClass kubelet")
	}
	imageID := osAlias
	instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("resolving instance types, %w", err), "InstanceTypeResolutionFailed", "Error resolving instance types")
	}

	if c.instanceProvider == nil {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("instance provider is nil"), "InstanceProviderNotConfigured", "Instance provider is not configured")
	}

	createdInstance, err := c.instanceProvider.Create(ctx, nodeClass, nodeClaim, instanceTypes)
	if err != nil {
		if cloudprovider.IsInsufficientCapacityError(err) || cloudprovider.IsNodeClassNotReadyError(err) {
			return nil, err
		}
		return nil, cloudprovider.NewCreateError(fmt.Errorf("creating instance, %w", err), "InstanceCreationFailed", "Error creating instance")
	}
	providerID := createdInstance.NodeID
	if providerID == "" {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("CreateNode succeeded but nodeID is empty"), "ProviderIDResolutionFailed", "Error resolving providerID")
	}
	instanceType, found := lo.Find(instanceTypes, func(it *cloudprovider.InstanceType) bool {
		return it.Name == createdInstance.Flavor
	})
	if !found {
		return nil, cloudprovider.NewCreateError(fmt.Errorf("selected instance type %q not found", createdInstance.Flavor), "InstanceTypeNotFound", "Selected instance type not found")
	}

	nc := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: resolvedNodeClaimLabels(instanceType, createdInstance),
			Annotations: map[string]string{
				v1alpha1.AnnotationNodeID:                  createdInstance.NodeID,
				v1alpha1.AnnotationInstanceID:              createdInstance.ServerID,
				v1alpha1.AnnotationCCENodeClassHash:        nodeClass.Hash(),
				v1alpha1.AnnotationCCENodeClassHashVersion: v1alpha1.CCENodeClassHashVersion,
			},
		},
		Status: karpv1.NodeClaimStatus{
			ProviderID:  providerID,
			ImageID:     imageID,
			Capacity:    instanceType.Capacity,
			Allocatable: instanceType.Allocatable(),
		},
	}
	return nc, nil
}

func resolvedNodeClaimLabels(instanceType *cloudprovider.InstanceType, createdInstance *instance.Instance) map[string]string {
	labels := map[string]string{
		corev1.LabelInstanceTypeStable: createdInstance.Flavor,
		corev1.LabelTopologyZone:       createdInstance.Zone,
		karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
	}
	for _, key := range resolvedNodeClaimLabelKeys {
		if value, ok := singleValueRequirement(instanceType.Requirements, key); ok {
			labels[key] = value
		}
	}
	return labels
}

func singleValueRequirement(requirements scheduling.Requirements, key string) (string, bool) {
	requirement, ok := requirements[key]
	if !ok || requirement.Operator() != corev1.NodeSelectorOpIn || requirement.Len() != 1 {
		return "", false
	}
	return requirement.Values()[0], true
}

// Delete is called by Karpenter to deprovision capacity for a NodeClaim.
func (c *CloudProvider) Delete(ctx context.Context, nodeClaim *karpv1.NodeClaim) error {
	if c.instanceProvider == nil {
		return fmt.Errorf("instance provider is nil")
	}
	if nodeClaim == nil || nodeClaim.Status.ProviderID == "" {
		return nil
	}
	return c.instanceProvider.Delete(ctx, nodeClaim.Status.ProviderID)
}

// Get returns the NodeClaim by provider ID.
func (c *CloudProvider) Get(ctx context.Context, providerID string) (*karpv1.NodeClaim, error) {
	if c.instanceProvider == nil {
		return nil, fmt.Errorf("instance provider is nil")
	}
	if _, err := c.instanceProvider.Get(ctx, providerID); err != nil {
		return nil, err
	}
	return &karpv1.NodeClaim{
		Status: karpv1.NodeClaimStatus{
			ProviderID: providerID,
		},
	}, nil
}

// List returns all NodeClaims managed by this provider.
func (c *CloudProvider) List(ctx context.Context) ([]*karpv1.NodeClaim, error) {
	if c.instanceProvider == nil {
		return nil, fmt.Errorf("instance provider is nil")
	}
	instances, err := c.instanceProvider.List(ctx)
	if err != nil {
		return nil, err
	}
	nodeClaims := make([]*karpv1.NodeClaim, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.NodeID == "" {
			continue
		}
		nodeClaims = append(nodeClaims, &karpv1.NodeClaim{
			Status: karpv1.NodeClaimStatus{
				ProviderID: inst.NodeID,
			},
		})
	}
	return nodeClaims, nil
}

// GetInstanceTypes returns the instance types available for a given NodePool.
func (c *CloudProvider) GetInstanceTypes(ctx context.Context, nodePool *karpv1.NodePool) ([]*cloudprovider.InstanceType, error) {
	nodeClass, err := c.resolveNodeClassFromNodePool(ctx, nodePool)
	if err != nil {
		if errors.IsNotFound(err) {
			c.recorder.Publish(events.Event{
				InvolvedObject: nodePool,
				Type:           corev1.EventTypeWarning,
				Reason:         "NodeClassNotFound",
				Message:        "Failed To Resolve NodeClass",
				DedupeValues:   []string{string(nodePool.UID)},
			})
			return nil, nil
		}
		return nil, fmt.Errorf("resolving nodeclass, %w", err)
	}
	instanceTypes, err := c.instanceTypeProvider.List(ctx, nodeClass)
	if err != nil {
		return nil, err
	}
	return instanceTypes, nil
}

// IsDrifted indicates whether the NodeClaim is drifted from desired state.
func (c *CloudProvider) IsDrifted(ctx context.Context, nodeClaim *karpv1.NodeClaim) (cloudprovider.DriftReason, error) {
	nodePoolName, ok := nodeClaim.Labels[karpv1.NodePoolLabelKey]
	if !ok {
		return "", nil
	}
	nodePool := &karpv1.NodePool{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodePoolName}, nodePool); err != nil {
		return "", client.IgnoreNotFound(err)
	}
	if nodePool.Spec.Template.Spec.NodeClassRef == nil {
		return "", nil
	}
	nodeClass, err := c.resolveNodeClassFromNodePool(ctx, nodePool)
	if err != nil {
		if errors.IsNotFound(err) {
			// We can't determine the drift status for the NodeClaim if we can no longer resolve the NodeClass
			c.recorder.Publish(events.Event{
				InvolvedObject: nodePool,
				Type:           corev1.EventTypeWarning,
				Message:        "Failed resolving NodeClass",
				DedupeValues:   []string{string(nodePool.UID)},
			})
			return "", nil
		}
		return "", fmt.Errorf("resolving nodeclass, %w", err)
	}

	driftReason, err := c.isNodeClassDrifted(ctx, nodeClaim, nodeClass)
	if err != nil {
		return "", err
	}
	return driftReason, nil
}

// RepairPolicies returns the node repair policies supported by this provider.
func (c *CloudProvider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{
		// Supported Kubelet Node Conditions
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionFalse,
			TolerationDuration: 30 * time.Minute,
		},
		{
			ConditionType:      corev1.NodeReady,
			ConditionStatus:    corev1.ConditionUnknown,
			TolerationDuration: 30 * time.Minute,
		},
	}
}

// Name returns the provider name.
func (c *CloudProvider) Name() string {
	return "huawei"
}

// GetSupportedNodeClasses returns the NodeClass types supported by this provider.
func (c *CloudProvider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.CCENodeClass{}}
}

func (c *CloudProvider) resolveNodeClassFromNodeClaim(ctx context.Context, nodeClaim *karpv1.NodeClaim) (*v1alpha1.CCENodeClass, error) {
	nodeClass := &v1alpha1.CCENodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodeClaim.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	if !nodeClass.DeletionTimestamp.IsZero() {
		// For the purposes of NodeClass CloudProvider resolution, we treat deleting NodeClasses as NotFound,
		// but we return a different error message to be clearer to users
		return nil, newTerminatingNodeClassError(nodeClass.Name)
	}
	return nodeClass, nil
}

func (c *CloudProvider) resolveNodeClassFromNodePool(ctx context.Context, nodePool *karpv1.NodePool) (*v1alpha1.CCENodeClass, error) {
	nodeClass := &v1alpha1.CCENodeClass{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Name: nodePool.Spec.Template.Spec.NodeClassRef.Name}, nodeClass); err != nil {
		return nil, err
	}
	if !nodeClass.DeletionTimestamp.IsZero() {
		// For the purposes of NodeClass CloudProvider resolution, we treat deleting NodeClasses as NotFound,
		// but we return a different error message to be clearer to users
		return nil, newTerminatingNodeClassError(nodeClass.Name)
	}
	return nodeClass, nil
}

// newTerminatingNodeClassError returns a NotFound error for handling by
func newTerminatingNodeClassError(name string) *errors.StatusError {
	qualifiedResource := schema.GroupResource{Group: apis.Group, Resource: "ccenodeclasses"}
	err := errors.NewNotFound(qualifiedResource, name)
	err.ErrStatus.Message = fmt.Sprintf("%s %q is terminating, treating as not found", qualifiedResource.String(), name)
	return err
}
