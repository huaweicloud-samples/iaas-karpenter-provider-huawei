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
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/sdkerr"
	cceMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/model"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
	sdk "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/huawei"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/subnet"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/utils"
)

const (
	cceFlavorInsufficientErrorCode   = "CCE_CM.0021"
	cceUnsupportedNetworkErrorCode   = "CCE.01400025"
	insufficientInSpecifiedAZMessage = "insufficient in specified az"
	insufficientCapacityMessage      = "insufficient capacity"
	eniNetworkNotSupportedMessage    = "Eni network is not supported"
	outOfStockMessage                = "out of stock"
	soldOutMessage                   = "sold out"
	sellOutMessage                   = "sell out"

	storageSelectorName          = "k8s-data"
	storageGroupName             = "vgpaas"
	defaultRuntimeStorageSize    = "90%"
	defaultKubernetesStorageSize = "10%"
	defaultStorageLVType         = "linear"

	minCreateNodeMaxPods        = int32(16)
	maxCreateNodeMaxPods        = int32(256)
	bytesPerMiB           int64 = 1024 * 1024
	bytesPerGiB           int64 = 1024 * 1024 * 1024
	maxCreateNodeIntValue       = int64(2147483647)
)

type reservedValues struct {
	CPU     *int32
	Memory  *int32
	PID     *int32
	Storage *int32
}

func (v reservedValues) Empty() bool {
	return v.CPU == nil && v.Memory == nil && v.PID == nil && v.Storage == nil
}

type Provider interface {
	Create(context.Context, *v1alpha1.CCENodeClass, *karpv1.NodeClaim, []*cloudprovider.InstanceType) (*Instance, error)
	Get(context.Context, string) (*Instance, error)
	List(context.Context) ([]*Instance, error)
	Delete(context.Context, string) error
}

type DefaultProvider struct {
	clusterID                 string
	cceapi                    sdk.CCEAPI
	subnetProvider            subnet.Provider
	offeringAvailabilityCache *utils.OfferingAvailabilityCache
}

func NewDefaultProvider(clusterID string, cceapi sdk.CCEAPI, subnetProvider subnet.Provider, offeringAvailabilityCache *utils.OfferingAvailabilityCache) Provider {
	return &DefaultProvider{
		clusterID:                 clusterID,
		cceapi:                    cceapi,
		subnetProvider:            subnetProvider,
		offeringAvailabilityCache: offeringAvailabilityCache,
	}
}

type createCandidate struct {
	instanceType *cloudprovider.InstanceType
	capacityType string
	price        float64
	zone         string
	subnetID     string
}

func (p *DefaultProvider) Create(ctx context.Context, nodeClass *v1alpha1.CCENodeClass, nodeClaim *karpv1.NodeClaim, instanceTypes []*cloudprovider.InstanceType) (*Instance, error) {
	logger := log.FromContext(ctx)

	if p.clusterID == "" {
		return nil, fmt.Errorf("CCE clusterID is empty")
	}
	osAlias, err := nodeClass.Spec.ResolveIMSForCreateNode()
	if err != nil {
		return nil, err
	}

	reqs := scheduling.NewNodeSelectorRequirementsWithMinValues(nodeClaim.Spec.Requirements...)
	// MVP: on-demand only
	if reqs.Has(karpv1.CapacityTypeLabelKey) && !reqs[karpv1.CapacityTypeLabelKey].Has(karpv1.CapacityTypeOnDemand) {
		return nil, fmt.Errorf("only %q is supported, got %q requirement", karpv1.CapacityTypeOnDemand, karpv1.CapacityTypeLabelKey)
	}
	reqs.Add(scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand))

	allowedInstanceTypes := sets.New[string]()
	if req, ok := lo.Find(nodeClaim.Spec.Requirements, func(r karpv1.NodeSelectorRequirementWithMinValues) bool {
		return r.Key == corev1.LabelInstanceTypeStable
	}); ok {
		allowedInstanceTypes.Insert(req.Values...)
	}

	compatibleInstanceTypes := lo.Filter(instanceTypes, func(it *cloudprovider.InstanceType, _ int) bool {
		if len(allowedInstanceTypes) > 0 && !allowedInstanceTypes.Has(it.Name) {
			return false
		}
		return len(it.Offerings.Compatible(reqs)) > 0
	})
	if len(compatibleInstanceTypes) == 0 {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no compatible instance types for requirements"))
	}

	selectedSubnet, err := p.subnetProvider.SelectForLaunch(ctx, nodeClass, compatibleInstanceTypes, karpv1.CapacityTypeOnDemand)
	if err != nil {
		return nil, err
	}
	defer p.subnetProvider.ReleaseInflightIPs(selectedSubnet)

	candidates := buildCreateCandidates(compatibleInstanceTypes, reqs, selectedSubnet)
	if len(candidates) == 0 {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("no compatible (instanceType, zone, subnet) candidates"))
	}
	filteredCandidates, skippedCandidates := p.filterUnavailableCandidates(candidates)
	if skippedCandidates > 0 {
		logger.WithValues("skipped", skippedCandidates, "remaining", len(filteredCandidates)).V(1).Info("skipping temporarily unavailable offerings for node creation")
	}
	if len(filteredCandidates) == 0 {
		return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("all compatible offerings are temporarily unavailable"))
	}

	var lastUnavailableErr error
	for _, c := range filteredCandidates {
		spec, err := nodeSpecForCandidate(nodeClass, nodeClaim, c, osAlias)
		if err != nil {
			return nil, err
		}
		resp, err := p.cceapi.CreateNode(&cceMdl.CreateNodeRequest{
			ClusterId: p.clusterID,
			Body: &cceMdl.NodeCreateRequest{
				Kind:       "Node",
				ApiVersion: "v3",
				Spec:       spec,
			},
		})
		if err != nil {
			var logMessage string
			switch {
			case isInsufficientCapacityError(err):
				logMessage = "marked offering temporarily unavailable after insufficient capacity"
			case isUnsupportedNetworkError(err):
				logMessage = "marked offering temporarily unavailable after unsupported network"
			default:
				return nil, err
			}
			if p.offeringAvailabilityCache != nil {
				p.offeringAvailabilityCache.MarkUnavailable(c.capacityType, sdk.InstanceType(c.instanceType.Name), c.zone)
				errorCode, errorMessage := serviceResponseErrorDetails(err)
				logger.WithValues(
					"capacity-type", c.capacityType,
					"instance-type", c.instanceType.Name,
					"zone", c.zone,
					"ttl", p.offeringAvailabilityCache.TTL().String(),
					"error-code", errorCode,
					"error-message", errorMessage,
				).V(1).Info(logMessage)
			}
			lastUnavailableErr = err
			continue
		}
		if resp == nil || resp.Metadata == nil || resp.Metadata.Uid == nil {
			return nil, fmt.Errorf("CreateNode succeeded but response metadata.uid is empty")
		}
		instance := &Instance{
			NodeID: lo.FromPtr(resp.Metadata.Uid),
			Flavor: c.instanceType.Name,
			Zone:   c.zone,
		}
		if resp.Status != nil {
			instance.ServerID = lo.FromPtrOr(resp.Status.ServerId, "")
		}
		return instance, nil
	}

	if lastUnavailableErr != nil {
		if isInsufficientCapacityError(lastUnavailableErr) {
			return nil, cloudprovider.NewInsufficientCapacityError(fmt.Errorf("CreateNode failed for all candidates, last error: %w", lastUnavailableErr))
		}
		return nil, fmt.Errorf("CreateNode failed for all candidates, last error: %w", lastUnavailableErr)
	}
	return nil, fmt.Errorf("CreateNode failed for all candidates")
}

func buildCreateCandidates(instanceTypes []*cloudprovider.InstanceType, reqs scheduling.Requirements, selectedSubnet *subnet.Subnet) []createCandidate {
	if selectedSubnet == nil || selectedSubnet.ID == "" {
		return nil
	}
	seen := map[string]struct{}{}
	candidates := make([]createCandidate, 0, len(instanceTypes))
	for _, it := range instanceTypes {
		for _, of := range it.Offerings.Compatible(reqs) {
			zoneReq, ok := of.Requirements[corev1.LabelTopologyZone]
			if !ok || zoneReq.Len() == 0 {
				continue
			}
			zone := zoneReq.Values()[0]
			capacityType := offeringCapacityType(of)
			key := capacityType + "/" + zone + "/" + it.Name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			candidates = append(candidates, createCandidate{
				instanceType: it,
				capacityType: capacityType,
				price:        of.Price,
				zone:         zone,
				subnetID:     selectedSubnet.ID,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].price != candidates[j].price {
			return candidates[i].price < candidates[j].price
		}
		if candidates[i].zone != candidates[j].zone {
			return candidates[i].zone < candidates[j].zone
		}
		return candidates[i].instanceType.Name < candidates[j].instanceType.Name
	})
	return candidates
}

func (p *DefaultProvider) filterUnavailableCandidates(candidates []createCandidate) ([]createCandidate, int) {
	filtered := make([]createCandidate, 0, len(candidates))
	skipped := 0
	for _, candidate := range candidates {
		if p.offeringAvailabilityCache.IsUnavailable(candidate.capacityType, sdk.InstanceType(candidate.instanceType.Name), candidate.zone) {
			skipped++
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered, skipped
}

func offeringCapacityType(offering *cloudprovider.Offering) string {
	capacityTypeReq, ok := offering.Requirements[karpv1.CapacityTypeLabelKey]
	if !ok || capacityTypeReq.Len() == 0 {
		return karpv1.CapacityTypeOnDemand
	}
	return capacityTypeReq.Values()[0]
}

func serviceResponseErrorDetails(err error) (string, string) {
	var serviceErr *sdkerr.ServiceResponseError
	if !errors.As(err, &serviceErr) {
		return "", summarizeErrorMessage(err.Error())
	}
	return serviceErr.ErrorCode, summarizeErrorMessage(serviceErr.ErrorMessage)
}

func summarizeErrorMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) <= 160 {
		return msg
	}
	return msg[:157] + "..."
}

func nodeSpecForCandidate(nodeClass *v1alpha1.CCENodeClass, nodeClaim *karpv1.NodeClaim, c createCandidate, osAlias string) (*cceMdl.NodeSpec, error) {
	rootVolume := toCCEVolume(&nodeClass.Spec.BlockDeviceMappings.Root)
	dataVolumes := resolveDataVolumes(nodeClass, rootVolume.Volumetype)
	storage := resolveStorage(nodeClass, rootVolume.Volumetype)
	login := resolveLogin(nodeClass)
	runtime := resolveRuntime(nodeClass)
	ecsGroupID := resolveECSGroupID(nodeClass)
	extendParam, err := resolveCreateNodeKubelet(nodeClass)
	if err != nil {
		return nil, err
	}

	k8sTags := map[string]string{
		"karpenter.k8s.huawei/nodeclaim-uid": string(nodeClaim.UID),
	}

	subnetID := c.subnetID
	var os *string
	if osAlias != "" {
		os = lo.ToPtr(osAlias)
	}
	spec := &cceMdl.NodeSpec{
		Flavor:      c.instanceType.Name,
		Az:          c.zone,
		Os:          os,
		Count:       lo.ToPtr(int32(1)),
		RootVolume:  rootVolume,
		DataVolumes: dataVolumes,
		Storage:     storage,
		Login:       login,
		Runtime:     runtime,
		ExtendParam: extendParam,
		EcsGroupId:  ecsGroupID,
		OffloadNode: lo.ToPtr(true),
		NodeNicSpec: &cceMdl.NodeNicSpec{
			PrimaryNic: &cceMdl.NicSpec{SubnetId: &subnetID},
		},
		Taints:  toCCETaints(nodeClaim),
		K8sTags: k8sTags,
	}
	return spec, nil
}

func ValidateKubeletForCreateNode(nodeClass *v1alpha1.CCENodeClass) error {
	if nodeClass == nil || nodeClass.Spec.Kubelet == nil {
		return nil
	}
	kubelet := nodeClass.Spec.Kubelet
	if kubelet.MaxPods != nil && (*kubelet.MaxPods < minCreateNodeMaxPods || *kubelet.MaxPods > maxCreateNodeMaxPods) {
		return fmt.Errorf("nodeClass.spec.kubelet.maxPods must be between %d and %d", minCreateNodeMaxPods, maxCreateNodeMaxPods)
	}
	if _, err := parseReservedValues("nodeClass.spec.kubelet.kubeReserved", kubelet.KubeReserved); err != nil {
		return err
	}
	if _, err := parseReservedValues("nodeClass.spec.kubelet.systemReserved", kubelet.SystemReserved); err != nil {
		return err
	}
	return nil
}

func resolveCreateNodeKubelet(nodeClass *v1alpha1.CCENodeClass) (*cceMdl.NodeExtendParam, error) {
	if nodeClass == nil || nodeClass.Spec.Kubelet == nil {
		return nil, nil
	}
	if err := ValidateKubeletForCreateNode(nodeClass); err != nil {
		return nil, err
	}
	kubeReserved, err := parseReservedValues("nodeClass.spec.kubelet.kubeReserved", nodeClass.Spec.Kubelet.KubeReserved)
	if err != nil {
		return nil, err
	}
	systemReserved, err := parseReservedValues("nodeClass.spec.kubelet.systemReserved", nodeClass.Spec.Kubelet.SystemReserved)
	if err != nil {
		return nil, err
	}
	if nodeClass.Spec.Kubelet.MaxPods == nil && kubeReserved.Empty() && systemReserved.Empty() {
		return nil, nil
	}
	return &cceMdl.NodeExtendParam{
		MaxPods:               nodeClass.Spec.Kubelet.MaxPods,
		KubeReservedCpu:       kubeReserved.CPU,
		KubeReservedMem:       kubeReserved.Memory,
		KubeReservedPid:       kubeReserved.PID,
		KubeReservedStorage:   kubeReserved.Storage,
		SystemReservedCpu:     systemReserved.CPU,
		SystemReservedMem:     systemReserved.Memory,
		SystemReservedPid:     systemReserved.PID,
		SystemReservedStorage: systemReserved.Storage,
	}, nil
}

func parseReservedValues(fieldPath string, values map[string]string) (reservedValues, error) {
	out := reservedValues{}
	for key, value := range values {
		keyPath := fieldPath + "." + key
		switch key {
		case string(corev1.ResourceCPU):
			v, err := parseQuantityAsMilliValue(keyPath, value)
			if err != nil {
				return reservedValues{}, err
			}
			out.CPU = lo.ToPtr(v)
		case string(corev1.ResourceMemory):
			v, err := parseQuantityAsBinaryUnitValue(keyPath, value, bytesPerMiB, "Mi")
			if err != nil {
				return reservedValues{}, err
			}
			out.Memory = lo.ToPtr(v)
		case string(corev1.ResourceEphemeralStorage):
			v, err := parseQuantityAsBinaryUnitValue(keyPath, value, bytesPerGiB, "Gi")
			if err != nil {
				return reservedValues{}, err
			}
			out.Storage = lo.ToPtr(v)
		case "pid":
			v, err := parsePIDValue(keyPath, value)
			if err != nil {
				return reservedValues{}, err
			}
			out.PID = lo.ToPtr(v)
		default:
			return reservedValues{}, fmt.Errorf("%s uses unsupported key %q", fieldPath, key)
		}
	}
	return out, nil
}

func parseQuantityAsMilliValue(fieldPath string, value string) (int32, error) {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid CPU quantity: %w", fieldPath, err)
	}
	milliValue := quantity.MilliValue()
	roundTrip := resource.MustParse(fmt.Sprintf("%dm", milliValue))
	if roundTrip.Cmp(quantity) != 0 {
		return 0, fmt.Errorf("%s must convert exactly to mcore", fieldPath)
	}
	return toCreateNodeInt32(fieldPath, milliValue)
}

func parseQuantityAsBinaryUnitValue(fieldPath string, value string, unitBytes int64, unitName string) (int32, error) {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid resource quantity: %w", fieldPath, err)
	}
	baseValue := quantity.Value()
	if baseValue%unitBytes != 0 {
		return 0, fmt.Errorf("%s must convert exactly to %s", fieldPath, unitName)
	}
	unitValue := baseValue / unitBytes
	roundTrip := resource.MustParse(fmt.Sprintf("%d%s", unitValue, unitName))
	if roundTrip.Cmp(quantity) != 0 {
		return 0, fmt.Errorf("%s must convert exactly to %s", fieldPath, unitName)
	}
	return toCreateNodeInt32(fieldPath, unitValue)
}

func parsePIDValue(fieldPath string, value string) (int32, error) {
	pidValue, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid integer: %w", fieldPath, err)
	}
	return toCreateNodeInt32(fieldPath, pidValue)
}

func toCreateNodeInt32(fieldPath string, value int64) (int32, error) {
	if value < 0 {
		return 0, fmt.Errorf("%s must be non-negative", fieldPath)
	}
	if value > maxCreateNodeIntValue {
		return 0, fmt.Errorf("%s exceeds the maximum supported value %d", fieldPath, maxCreateNodeIntValue)
	}
	return int32(value), nil
}

func resolveDataVolumes(nodeClass *v1alpha1.CCENodeClass, rootVolumeType string) *[]cceMdl.Volume {
	volumes := make([]cceMdl.Volume, 0, 1+len(nodeClass.Spec.BlockDeviceMappings.Users))
	if nodeClass.Spec.BlockDeviceMappings.K8S != nil {
		volumes = append(volumes, *toCCEVolume(nodeClass.Spec.BlockDeviceMappings.K8S))
	} else {
		volumes = append(volumes, cceMdl.Volume{
			Size:       v1alpha1.DefaultKubernetesVolumeSizeGiB,
			Volumetype: rootVolumeType,
		})
	}
	for i := range nodeClass.Spec.BlockDeviceMappings.Users {
		volumes = append(volumes, *toCCEVolume(&nodeClass.Spec.BlockDeviceMappings.Users[i]))
	}
	return lo.ToPtr(volumes)
}

func resolveStorage(nodeClass *v1alpha1.CCENodeClass, rootVolumeType string) *cceMdl.Storage {
	managedVolume := cceMdl.Volume{
		Size:       v1alpha1.DefaultKubernetesVolumeSizeGiB,
		Volumetype: rootVolumeType,
	}
	if nodeClass.Spec.BlockDeviceMappings.K8S != nil {
		managedVolume = *toCCEVolume(nodeClass.Spec.BlockDeviceMappings.K8S)
	}
	matchLabels := &cceMdl.StorageSelectorsMatchLabels{
		Size:       lo.ToPtr(fmt.Sprint(managedVolume.Size)),
		VolumeType: lo.ToPtr(managedVolume.Volumetype),
		Count:      lo.ToPtr("1"),
	}
	if managedVolume.Iops != nil {
		matchLabels.Iops = lo.ToPtr(fmt.Sprint(*managedVolume.Iops))
	}
	if managedVolume.Throughput != nil {
		matchLabels.Throughput = lo.ToPtr(fmt.Sprint(*managedVolume.Throughput))
	}
	return &cceMdl.Storage{
		StorageSelectors: []cceMdl.StorageSelectors{{
			Name:        storageSelectorName,
			StorageType: "evs",
			MatchLabels: matchLabels,
		}},
		StorageGroups: []cceMdl.StorageGroups{{
			Name:          storageGroupName,
			CceManaged:    lo.ToPtr(true),
			SelectorNames: []string{storageSelectorName},
			VirtualSpaces: []cceMdl.VirtualSpace{
				{
					Name: "runtime",
					Size: defaultRuntimeStorageSize,
					RuntimeConfig: &cceMdl.RuntimeConfig{
						LvType: defaultStorageLVType,
					},
				},
				{
					Name: "kubernetes",
					Size: defaultKubernetesStorageSize,
					LvmConfig: &cceMdl.LvmConfig{
						LvType: defaultStorageLVType,
					},
				},
			},
		}},
	}
}

func toCCEVolume(device *v1alpha1.BlockDevice) *cceMdl.Volume {
	volumeType := device.VolumeType
	if volumeType == "" {
		volumeType = "SSD"
	}
	size := device.VolumeSize
	if size <= 0 {
		size = 40
	}
	return &cceMdl.Volume{
		Size:       size,
		Volumetype: volumeType,
		Iops:       device.IOPS,
		Throughput: device.Throughput,
	}
}

func resolveLogin(nodeClass *v1alpha1.CCENodeClass) *cceMdl.Login {
	if nodeClass == nil {
		return nil
	}
	if sshKey := nodeClass.Spec.Login.SSHKey; sshKey != "" {
		return &cceMdl.Login{
			SshKey: lo.ToPtr(sshKey),
		}
	}
	if nodeClass.Spec.Login.UserPassword == nil || nodeClass.Spec.Login.UserPassword.Password == "" {
		return nil
	}
	username := nodeClass.Spec.Login.UserPassword.Username
	if username == "" {
		username = "root"
	}
	return &cceMdl.Login{
		UserPassword: &cceMdl.UserPassword{
			Username: lo.ToPtr(username),
			Password: nodeClass.Spec.Login.UserPassword.Password,
		},
	}
}

func resolveRuntime(nodeClass *v1alpha1.CCENodeClass) *cceMdl.Runtime {
	if nodeClass.Spec.RuntimeConfiguration == nil {
		return nil
	}
	runtimeType := nodeClass.Spec.RuntimeConfiguration.Type
	if runtimeType == "" {
		return nil
	}
	enums := cceMdl.GetRuntimeNameEnum()
	runtimeName := enums.CONTAINERD
	if runtimeType == "docker" {
		runtimeName = enums.DOCKER
	}
	return &cceMdl.Runtime{Name: &runtimeName}
}

func resolveECSGroupID(nodeClass *v1alpha1.CCENodeClass) *string {
	if nodeClass.Spec.ECSGroupID == nil {
		return nil
	}
	if value := *nodeClass.Spec.ECSGroupID; value != "" {
		return lo.ToPtr(value)
	}
	return nil
}

func toCCETaints(nodeClaim *karpv1.NodeClaim) *[]cceMdl.Taint {
	var taints []corev1.Taint
	taints = append(taints, karpv1.UnregisteredNoExecuteTaint)
	taints = append(taints, nodeClaim.Spec.StartupTaints...)
	taints = append(taints, nodeClaim.Spec.Taints...)
	// CCE manages node.kubernetes.io/network-unavailable internally and rejects CreateNode
	// requests that try to set it explicitly.
	taints = lo.Reject(taints, func(t corev1.Taint, _ int) bool {
		return t.MatchTaint(&corev1.Taint{
			Key:    corev1.TaintNodeNetworkUnavailable,
			Effect: corev1.TaintEffectNoSchedule,
		})
	})

	// Deduplicate by (key,effect)
	deduped := map[string]corev1.Taint{}
	for _, t := range taints {
		key := t.Key + "/" + string(t.Effect)
		if key == karpv1.UnregisteredTaintKey+"/"+string(corev1.TaintEffectNoExecute) {
			deduped[key] = karpv1.UnregisteredNoExecuteTaint
			continue
		}
		deduped[key] = t
	}

	out := make([]cceMdl.Taint, 0, len(deduped))
	for _, t := range deduped {
		var v *string
		if t.Value != "" {
			v = lo.ToPtr(t.Value)
		}
		out = append(out, cceMdl.Taint{
			Key:    t.Key,
			Value:  v,
			Effect: toCCETaintEffect(t.Effect),
		})
	}
	return lo.ToPtr(out)
}

func toCCETaintEffect(effect corev1.TaintEffect) cceMdl.TaintEffect {
	enums := cceMdl.GetTaintEffectEnum()
	switch effect {
	case corev1.TaintEffectPreferNoSchedule:
		return enums.PREFER_NO_SCHEDULE
	case corev1.TaintEffectNoExecute:
		return enums.NO_EXECUTE
	case corev1.TaintEffectNoSchedule:
		fallthrough
	default:
		return enums.NO_SCHEDULE
	}
}

func (p *DefaultProvider) Get(ctx context.Context, providerID string) (*Instance, error) {
	nodeID, err := nodeIDFromProviderID(providerID)
	if err != nil {
		return nil, err
	}
	resp, err := p.cceapi.ShowNode(&cceMdl.ShowNodeRequest{
		ClusterId: p.clusterID,
		NodeId:    nodeID,
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}
		return nil, err
	}
	if resp == nil {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("CCE node not found for nodeId=%q", nodeID))
	}
	return instanceFromCCENodeParts(resp.Metadata, resp.Spec, resp.Status), nil
}

func (p *DefaultProvider) List(ctx context.Context) ([]*Instance, error) {
	resp, err := p.cceapi.ListNodes(&cceMdl.ListNodesRequest{ClusterId: p.clusterID})
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Items == nil {
		return nil, nil
	}
	items := lo.FromPtr(resp.Items)
	out := make([]*Instance, 0, len(items))
	for _, node := range items {
		inst := instanceFromCCENodeParts(node.Metadata, node.Spec, node.Status)
		if inst.NodeID == "" {
			continue
		}
		out = append(out, inst)
	}
	return out, nil
}

func (p *DefaultProvider) Delete(ctx context.Context, providerID string) error {
	nodeID, err := nodeIDFromProviderID(providerID)
	if err != nil {
		return err
	}
	_, err = p.cceapi.DeleteNode(&cceMdl.DeleteNodeRequest{
		ClusterId: p.clusterID,
		NodeId:    nodeID,
	})
	if err != nil {
		if isNotFoundError(err) {
			return cloudprovider.NewNodeClaimNotFoundError(err)
		}
		return err
	}
	return nil
}

func instanceFromCCENodeParts(metadata *cceMdl.NodeMetadata, spec *cceMdl.NodeSpec, status *cceMdl.NodeStatus) *Instance {
	out := &Instance{}
	if metadata != nil && metadata.Uid != nil {
		out.NodeID = lo.FromPtr(metadata.Uid)
	}
	if status != nil && status.ServerId != nil {
		out.ServerID = lo.FromPtr(status.ServerId)
	}
	if spec != nil {
		out.Flavor = spec.Flavor
		out.Zone = spec.Az
		if spec.NodeNicSpec != nil && spec.NodeNicSpec.PrimaryNic != nil && spec.NodeNicSpec.PrimaryNic.SubnetId != nil {
			out.SubnetID = lo.FromPtr(spec.NodeNicSpec.PrimaryNic.SubnetId)
		}
	}
	return out
}

func nodeIDFromProviderID(providerID string) (string, error) {
	if providerID == "" {
		return "", fmt.Errorf("providerID is empty")
	}
	return providerID, nil
}

func isInsufficientCapacityError(err error) bool {
	var serviceErr *sdkerr.ServiceResponseError
	if !errors.As(err, &serviceErr) {
		return false
	}
	code := serviceErr.ErrorCode
	msg := serviceErr.ErrorMessage

	if code != "" {
		return code == cceFlavorInsufficientErrorCode
	}
	for _, s := range []string{
		insufficientInSpecifiedAZMessage,
		insufficientCapacityMessage,
		outOfStockMessage,
		soldOutMessage,
		sellOutMessage,
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func isUnsupportedNetworkError(err error) bool {
	var serviceErr *sdkerr.ServiceResponseError
	if !errors.As(err, &serviceErr) {
		return false
	}
	code := serviceErr.ErrorCode
	msg := serviceErr.ErrorMessage
	if code == cceUnsupportedNetworkErrorCode {
		return true
	}
	return strings.Contains(msg, eniNetworkNotSupportedMessage)
}

func isNotFoundError(err error) bool {
	var serviceErr *sdkerr.ServiceResponseError
	if errors.As(err, &serviceErr) {
		return serviceErr.StatusCode == 404
	}
	return false
}
