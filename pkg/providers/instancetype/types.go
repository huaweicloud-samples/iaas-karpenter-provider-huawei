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

package instancetype

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	ecsMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/model"
	"github.com/mitchellh/hashstructure/v2"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
	"sigs.k8s.io/karpenter/pkg/utils/resources"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
)

const (
	ChargeTypeSpot     string = "spot"
	ChargeTypeOnDemand string = "on-demand"

	defaultRuntimeType                   = "containerd"
	dockerRuntimeType                    = "docker"
	defaultNodeFSEvictionThreshold       = "10%"
	bytesPerGiB                    int64 = 1024 * 1024 * 1024
	lvmExtentSizeBytes             int64 = 4 * 1024 * 1024
	ext4BlockSizeBytes             int64 = 4096
	dataVolumeMetadataReservedMiB  int64 = 4
	// Keep this aligned with pkg/providers/instance/instance.go defaultKubernetesStorageSize.
	defaultKubernetesVirtualSpacePercent int64 = 10
	ext4BlocksPerGroup                   int64 = 32768
	ext4GroupsPerDescriptorBlock         int64 = 64
	ext4InodeBlocksPerGroup              int64 = 512
	ext4PointersPerBlock                 int64 = ext4BlockSizeBytes / 4
	systemReservedBaseMemoryMiB          int64 = 400
	systemReservedPerGiBMiB              int64 = 25
	kubeReservedBaseMemoryMiB            int64 = 500
	dockerMemoryPerPodMiB                int64 = 20
	containerdMemoryPerPodMiB            int64 = 5
)

type Resolver interface {
	// CacheKey tells the InstanceType cache if something changes about the InstanceTypes or Offerings based on the NodeClass.
	CacheKey(NodeClass) string
	// Resolve generates an InstanceType based on raw Flavor and NodeClass setting data
	Resolve(ctx context.Context, info ecsMdl.Flavor, zones []string, nodeClass NodeClass) *cloudprovider.InstanceType
}

type DefaultResolver struct {
	region string
}

func NewDefaultResolver(region string) *DefaultResolver {
	return &DefaultResolver{
		region: region,
	}
}

func (d *DefaultResolver) CacheKey(nodeClass NodeClass) string {
	kc := &v1alpha1.KubeletConfiguration{}
	if resolved := nodeClass.KubeletConfiguration(); resolved != nil {
		kc = resolved
	}
	kcHash, _ := hashstructure.Hash(struct {
		Kubelet             *v1alpha1.KubeletConfiguration
		RuntimeType         string
		BlockDeviceMappings v1alpha1.BlockDeviceMappings
	}{
		Kubelet:             kc,
		RuntimeType:         normalizedRuntimeType(nodeClass.RuntimeConfiguration()),
		BlockDeviceMappings: nodeClass.BlockDeviceMappings(),
	}, hashstructure.FormatV2, &hashstructure.HashOptions{SlicesAsSets: true})
	return fmt.Sprintf("%016x", kcHash)
}

func (d *DefaultResolver) Resolve(ctx context.Context, info ecsMdl.Flavor, zones []string, nodeClass NodeClass) *cloudprovider.InstanceType {
	// !!! Important !!!
	// Any changes to the values passed into the NewInstanceType method will require making updates to the cache key
	// so that Karpenter is able to cache the set of InstanceTypes based on values that alter the set of instance types
	// !!! Important !!!
	kc := &v1alpha1.KubeletConfiguration{}
	if resolved := nodeClass.KubeletConfiguration(); resolved != nil {
		kc = resolved
	}
	runtimeType := normalizedRuntimeType(nodeClass.RuntimeConfiguration())
	return NewInstanceType(
		info,
		d.region,
		zones,
		runtimeType,
		kc.MaxPods,
		nodeClass.BlockDeviceMappings(),
		kc.KubeReserved,
		kc.SystemReserved,
	)
}

func NewInstanceType(
	info ecsMdl.Flavor,
	region string,
	offeringZones []string,
	runtimeType string,
	maxPods *int32,
	blockDeviceMappings v1alpha1.BlockDeviceMappings,
	kubeReserved map[string]string,
	systemReserved map[string]string,
) *cloudprovider.InstanceType {
	it := &cloudprovider.InstanceType{
		Name:         info.Name,
		Requirements: computeRequirements(info, region, offeringZones),
		Capacity:     computeCapacity(info, maxPods, blockDeviceMappings),
		Overhead: &cloudprovider.InstanceTypeOverhead{
			KubeReserved:      kubeReservedResources(cpu(info), int64(info.Ram), runtimeType, kubeReserved),
			SystemReserved:    systemReservedResources(int64(info.Ram), systemReserved),
			EvictionThreshold: evictionThreshold(ephemeralStorage(blockDeviceMappings)),
		},
	}
	return it
}

func computeRequirements(info ecsMdl.Flavor, region string, offeringZones []string) scheduling.Requirements {
	capacityTypes := []string{ChargeTypeOnDemand}
	availableZones := sets.New(offeringZones...)
	attributes := deriveFlavorAttributes(info)

	requirements := scheduling.NewRequirements(
		// Well Known Upstream
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, info.Name),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, getArchitecture(info)),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, string(corev1.Linux)),
		scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region),
		scheduling.NewRequirement(corev1.LabelWindowsBuild, corev1.NodeSelectorOpDoesNotExist),
		// Well Known to Karpenter
		scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, capacityTypes...),
		// Well Known to Huawei
		derivedRequirement(v1alpha1.LabelInstanceCategory, attributes.category),
		derivedRequirement(v1alpha1.LabelInstanceFamily, attributes.family),
		derivedRequirement(v1alpha1.LabelInstanceGeneration, attributes.generation),
		derivedRequirement(v1alpha1.LabelInstanceCPU, attributes.cpu),
		derivedRequirement(v1alpha1.LabelInstanceMemory, attributes.memory),
		derivedRequirement(v1alpha1.LabelInstanceSize, attributes.size),
		scheduling.NewRequirement(v1alpha1.LabelInstanceNetworkBandwidth, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceGPUName, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceGPUManufacturer, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceGPUCount, corev1.NodeSelectorOpDoesNotExist),
		scheduling.NewRequirement(v1alpha1.LabelInstanceGPUMemory, corev1.NodeSelectorOpDoesNotExist),
	)
	if availableZones.Len() != 0 {
		requirements.Add(scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, availableZones.UnsortedList()...))
	}
	return requirements
}

type derivedAttribute struct {
	value string
	known bool
}

type flavorAttributes struct {
	category   derivedAttribute
	family     derivedAttribute
	generation derivedAttribute
	cpu        derivedAttribute
	memory     derivedAttribute
	size       derivedAttribute
}

var huaweiMainSeries = sets.New[string]("t", "s", "x", "c", "h", "d", "i", "ir", "m", "e", "p", "g", "pi", "fp", "ai")

func deriveFlavorAttributes(info ecsMdl.Flavor) flavorAttributes {
	attributes := flavorAttributes{}
	generationName := info.Name
	if parts := strings.SplitN(info.Name, ".", 2); len(parts) > 0 {
		generationName = parts[0]
	}
	attributes.family, attributes.generation, attributes.category = parseGenerationName(generationName)
	attributes.cpu = canonicalPositiveDecimal(info.Vcpus)
	if info.Ram > 0 {
		attributes.memory.value = strconv.Itoa(int(info.Ram))
		attributes.memory.known = validFlavorLabelValue(attributes.memory.value)
	}
	attributes.size = parseFlavorSize(info.Name)
	return attributes
}

func derivedRequirement(key string, attribute derivedAttribute) *scheduling.Requirement {
	if !attribute.known {
		return scheduling.NewRequirement(key, corev1.NodeSelectorOpDoesNotExist)
	}
	return scheduling.NewRequirement(key, corev1.NodeSelectorOpIn, attribute.value)
}

func parseGenerationName(name string) (family, generation, category derivedAttribute) {
	if name == "" {
		return derivedAttribute{}, derivedAttribute{}, derivedAttribute{}
	}

	digitStart, digitEnd := -1, -1
	for i := 0; i < len(name); i++ {
		isDigit := name[i] >= '0' && name[i] <= '9'
		if !isDigit {
			if digitStart != -1 && digitEnd == -1 {
				digitEnd = i
			}
			continue
		}
		if digitStart == -1 {
			digitStart = i
			continue
		}
		if digitEnd != -1 {
			return derivedAttribute{}, derivedAttribute{}, derivedAttribute{}
		}
	}
	if digitStart == -1 {
		return derivedAttribute{}, derivedAttribute{}, derivedAttribute{}
	}
	if digitEnd == -1 {
		digitEnd = len(name)
	}
	if digitStart == 0 || digitEnd == digitStart || name[digitStart] == '0' {
		return derivedAttribute{}, derivedAttribute{}, derivedAttribute{}
	}
	for i, char := range []byte(name) {
		if i >= digitStart && i < digitEnd {
			continue
		}
		if char < 'a' || char > 'z' {
			return derivedAttribute{}, derivedAttribute{}, derivedAttribute{}
		}
	}

	family.value = name
	family.known = validFlavorLabelValue(family.value)
	generation = canonicalPositiveDecimal(name[digitStart:digitEnd])
	prefix := name[:digitStart]
	category.value, category.known = huaweiCategory(prefix)
	if category.known && !validFlavorLabelValue(category.value) {
		category = derivedAttribute{}
	}
	return family, generation, category
}

func huaweiCategory(prefix string) (string, bool) {
	if huaweiMainSeries.Has(prefix) {
		return prefix, true
	}
	if strings.HasPrefix(prefix, "k") {
		category := strings.TrimPrefix(prefix, "k")
		if huaweiMainSeries.Has(category) {
			return category, true
		}
	}
	// Huawei's aC family is explicitly classified as Compute even though the
	// current generic naming table only documents x86 and Kunpeng prefixes.
	if prefix == "ac" {
		return "c", true
	}
	return "", false
}

func parseFlavorSize(name string) derivedAttribute {
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return derivedAttribute{}
	}
	size := parts[1]
	switch size {
	case "small", "medium", "large", "xlarge":
		return derivedAttribute{value: size, known: true}
	}
	if !strings.HasSuffix(size, "xlarge") {
		return derivedAttribute{}
	}
	count := strings.TrimSuffix(size, "xlarge")
	if !canonicalPositiveDecimal(count).known {
		return derivedAttribute{}
	}
	return derivedAttribute{value: size, known: validFlavorLabelValue(size)}
}

func canonicalPositiveDecimal(value string) derivedAttribute {
	if value == "" || value[0] == '0' {
		return derivedAttribute{}
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 || strconv.Itoa(parsed) != value {
		return derivedAttribute{}
	}
	return derivedAttribute{value: value, known: validFlavorLabelValue(value)}
}

func validFlavorLabelValue(value string) bool {
	return len(utilvalidation.IsValidLabelValue(value)) == 0
}

func computeCapacity(info ecsMdl.Flavor, maxPods *int32, blockDeviceMappings v1alpha1.BlockDeviceMappings) corev1.ResourceList {
	resourceList := corev1.ResourceList{
		corev1.ResourceCPU:              *cpu(info),
		corev1.ResourceMemory:           *memory(info),
		corev1.ResourcePods:             *pods(info, maxPods),
		corev1.ResourceEphemeralStorage: *ephemeralStorage(blockDeviceMappings),
	}
	return resourceList
}

func getArchitecture(info ecsMdl.Flavor) string {
	if info.OsExtraSpecs == nil || info.OsExtraSpecs.EcsinstanceArchitecture == nil {
		return "amd64"
	}
	return *info.OsExtraSpecs.EcsinstanceArchitecture
}

func cpu(info ecsMdl.Flavor) *resource.Quantity {
	return resources.Quantity(info.Vcpus)
}

func memory(info ecsMdl.Flavor) *resource.Quantity {
	return resources.Quantity(fmt.Sprintf("%dMi", info.Ram))
}

func ephemeralStorage(blockDeviceMappings v1alpha1.BlockDeviceMappings) *resource.Quantity {
	sizeGiB := v1alpha1.DefaultKubernetesVolumeSizeGiB
	if blockDeviceMappings.K8S != nil && blockDeviceMappings.K8S.VolumeSize > 0 {
		sizeGiB = blockDeviceMappings.K8S.VolumeSize
	}

	// CCE's default storage layout splits the managed data volume into runtime and kubernetes LVs.
	// kubelet reports nodefs from /mnt/paas/kubernetes/kubelet (kubernetes LV), not the full managed LV.
	lvSizeBytes := int64(sizeGiB)*bytesPerGiB - dataVolumeMetadataReservedMiB*1024*1024
	kubernetesLVSizeBytes := kubernetesLVSize(lvSizeBytes)
	return resource.NewQuantity(ext4FilesystemSizeBytes(kubernetesLVSizeBytes), resource.BinarySI)
}

func ext4FilesystemSizeBytes(deviceSizeBytes int64) int64 {
	if deviceSizeBytes <= 0 {
		return 0
	}
	blockCount := deviceSizeBytes / ext4BlockSizeBytes
	if blockCount == 0 {
		return 0
	}
	groupCount := ceilDiv(blockCount, ext4BlocksPerGroup)
	descriptorBlocks := ceilDiv(groupCount, ext4GroupsPerDescriptorBlock)
	backupSuperGroupCount := sparseSuperGroupCopies(groupCount)
	reservedGDTBlocks := ext4ReservedGDTBlocks(blockCount, descriptorBlocks)
	journalBlocks := ext4DefaultJournalBlocks(blockCount)

	// Match e2fsprogs (mke2fs 1.47.0) overhead accounting for ext4 with:
	// - base block/inode bitmaps + inode table in every group
	// - superblock/GDT/reserved GDT in backup-super groups
	// - default internal journal size
	overheadBlocks := groupCount*(2+ext4InodeBlocksPerGroup) +
		backupSuperGroupCount*(1+descriptorBlocks+reservedGDTBlocks) +
		journalBlocks
	return deviceSizeBytes - overheadBlocks*ext4BlockSizeBytes
}

func kubernetesLVSize(managedLVSizeBytes int64) int64 {
	if managedLVSizeBytes <= 0 {
		return 0
	}
	alignedManaged := alignDown(managedLVSizeBytes, lvmExtentSizeBytes)
	kubernetesBytes := (alignedManaged * defaultKubernetesVirtualSpacePercent) / 100
	return alignDown(kubernetesBytes, lvmExtentSizeBytes)
}

func alignDown(v, unit int64) int64 {
	if v <= 0 || unit <= 0 {
		return 0
	}
	return (v / unit) * unit
}

func sparseSuperGroupCopies(groupCount int64) int64 {
	if groupCount <= 0 {
		return 0
	}
	copies := int64(2)
	for _, base := range []int64{3, 5, 7} {
		for group := base; group < groupCount; {
			copies++
			if group > (groupCount-1)/base {
				break
			}
			group *= base
		}
	}
	return copies
}

func ext4ReservedGDTBlocks(blockCount, descriptorBlocks int64) int64 {
	if blockCount <= 0 {
		return 0
	}
	maxBlocks := int64(0xffffffff)
	if blockCount < maxBlocks/1024 {
		maxBlocks = blockCount * 1024
	}
	reservedGroups := ceilDiv(maxBlocks, ext4BlocksPerGroup)
	reservedGDTBlocks := ceilDiv(reservedGroups, ext4GroupsPerDescriptorBlock) - descriptorBlocks
	if reservedGDTBlocks < 0 {
		return 0
	}
	return minInt64(reservedGDTBlocks, ext4PointersPerBlock)
}

func ext4DefaultJournalBlocks(blockCount int64) int64 {
	switch {
	case blockCount < 2048:
		return 0
	case blockCount < 32768:
		return 1024
	case blockCount < 256*1024:
		return 4096
	case blockCount < 512*1024:
		return 8192
	case blockCount < 4096*1024:
		return 16384
	case blockCount < 8192*1024:
		return 32768
	case blockCount < 16384*1024:
		return 65536
	case blockCount < 32768*1024:
		return 131072
	default:
		return 262144
	}
}

func ceilDiv(a, b int64) int64 {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

func pods(info ecsMdl.Flavor, maxPods *int32) *resource.Quantity {
	var count int64
	switch {
	case maxPods != nil:
		count = int64(lo.FromPtr(maxPods))
	default:
		count = defaultMaxPods(info)
	}
	return resources.Quantity(fmt.Sprint(count))
}

func defaultMaxPods(info ecsMdl.Flavor) int64 {
	count := defaultMaxPodsByMemoryMiB(int64(info.Ram))
	if info.OsExtraSpecs == nil {
		return count
	}
	if nicCap, ok := parsePositiveInt64(info.OsExtraSpecs.QuotasubNetworkInterfaceMaxNum); ok {
		return minInt64(count, nicCap)
	}
	return count
}

func defaultMaxPodsByMemoryMiB(memoryMiB int64) int64 {
	switch {
	case memoryMiB >= 65536:
		return 110
	case memoryMiB >= 32768:
		return 80
	case memoryMiB >= 16384:
		return 60
	case memoryMiB >= 8192:
		return 40
	default:
		return 20
	}
}

func parsePositiveInt64(v *string) (int64, bool) {
	if v == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(*v), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func normalizedRuntimeType(runtimeConfiguration *v1alpha1.RuntimeConfiguration) string {
	if runtimeConfiguration != nil && runtimeConfiguration.Type != "" {
		return runtimeConfiguration.Type
	}
	return defaultRuntimeType
}

func systemReservedMemoryMiB(memoryMiB int64) int64 {
	return systemReservedBaseMemoryMiB + (memoryMiB*systemReservedPerGiBMiB)/1024
}

func kubeReservedMemoryMiB(memoryMiB int64, runtimeType string) int64 {
	perPodMiB := containerdMemoryPerPodMiB
	switch runtimeType {
	case dockerRuntimeType:
		perPodMiB = dockerMemoryPerPodMiB
	}
	// CCE's default v2 kubeReserved memory formula uses the memory-tier default
	// pod count rather than the node's effective pod capacity.
	podCount := defaultMaxPodsByMemoryMiB(memoryMiB)
	return kubeReservedBaseMemoryMiB + (perPodMiB * podCount)
}

func systemReservedResources(memoryMiB int64, systemReserved map[string]string) corev1.ResourceList {
	resources := corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", systemReservedMemoryMiB(memoryMiB))),
	}
	return lo.Assign(resources, lo.MapEntries(systemReserved, func(k string, v string) (corev1.ResourceName, resource.Quantity) {
		return corev1.ResourceName(k), resource.MustParse(v)
	}))
}

func evictionThreshold(ephemeralStorage *resource.Quantity) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceMemory:           resource.MustParse("100Mi"),
		corev1.ResourceEphemeralStorage: computeEvictionSignal(*ephemeralStorage, defaultNodeFSEvictionThreshold),
	}
}

// computeEvictionSignal computes the resource quantity value for an eviction signal value, computed off the
// base capacity value if the signal value is a percentage or as a resource quantity if the signal value isn't a percentage
func computeEvictionSignal(capacity resource.Quantity, signalValue string) resource.Quantity {
	if strings.HasSuffix(signalValue, "%") {
		p := mustParsePercentage(signalValue)
		return *resource.NewQuantity(int64(float64(capacity.Value())*float64(p)), resource.BinarySI)
	}
	return resource.MustParse(signalValue)
}

func mustParsePercentage(v string) float32 {
	p, err := strconv.ParseFloat(strings.Trim(v, "%"), 32)
	if err != nil {
		panic(fmt.Sprintf("expected percentage value to be a float but got %s, %v", v, err))
	}
	// Setting percentage value to 100% is considered disabling the threshold according to
	// https://kubernetes.io/docs/reference/config-api/kubelet-config.v1beta1/
	if p == 100 {
		return 0
	}
	return float32(p) / 100
}

func kubeReservedResources(cpus *resource.Quantity, memoryMiB int64, runtimeType string, kubeReserved map[string]string) corev1.ResourceList {
	resources := corev1.ResourceList{
		corev1.ResourceMemory: resource.MustParse(fmt.Sprintf("%dMi", kubeReservedMemoryMiB(memoryMiB, runtimeType))),
	}
	// CPU reservation follows the CCE node CPU reservation tiers.
	for _, cpuRange := range []struct {
		start      int64
		end        int64
		percentage float64
	}{
		{start: 0, end: 1000, percentage: 0.06},
		{start: 1000, end: 2000, percentage: 0.01},
		{start: 2000, end: 4000, percentage: 0.005},
		{start: 4000, end: 1 << 31, percentage: 0.0025},
	} {
		if cpu := cpus.MilliValue(); cpu >= cpuRange.start {
			r := float64(cpuRange.end - cpuRange.start)
			if cpu < cpuRange.end {
				r = float64(cpu - cpuRange.start)
			}
			cpuOverhead := resources.Cpu()
			cpuOverhead.Add(*resource.NewMilliQuantity(int64(r*cpuRange.percentage), resource.DecimalSI))
			resources[corev1.ResourceCPU] = *cpuOverhead
		}
	}
	return lo.Assign(resources, lo.MapEntries(kubeReserved, func(k string, v string) (corev1.ResourceName, resource.Quantity) {
		return corev1.ResourceName(k), resource.MustParse(v)
	}))
}
