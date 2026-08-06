package instancetype

import (
	"fmt"
	"strings"
	"testing"

	ecsMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
)

func TestNewInstanceType_ExposesHuaweiFlavorRequirements(t *testing.T) {
	testCases := []struct {
		name       string
		flavorName string
		category   string
		family     string
		generation string
		cpu        string
		memory     string
		size       string
	}{
		{name: "c7", flavorName: "c7.large.2", category: "c", family: "c7", generation: "7", cpu: "2", memory: "4096", size: "large"},
		{name: "c7h", flavorName: "c7h.xlarge.2", category: "c", family: "c7h", generation: "7", cpu: "4", memory: "8192", size: "xlarge"},
		{name: "kunpeng c1", flavorName: "kc1.small.1", category: "c", family: "kc1", generation: "1", cpu: "1", memory: "1024", size: "small"},
		{name: "aC9", flavorName: "ac9.2xlarge.2", category: "c", family: "ac9", generation: "9", cpu: "8", memory: "16384", size: "2xlarge"},
		{name: "kunpeng x1 without size", flavorName: "kx1", category: "x", family: "kx1", generation: "1", cpu: "4", memory: "8192"},
		{name: "multi-digit generation", flavorName: "c10e.medium.4", category: "c", family: "c10e", generation: "10", cpu: "1", memory: "4096", size: "medium"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			info := ecsMdl.Flavor{Name: tc.flavorName, Vcpus: tc.cpu, Ram: 0}
			if tc.memory != "" {
				var ram int32
				if _, err := fmt.Sscan(tc.memory, &ram); err != nil {
					t.Fatalf("parsing test memory: %v", err)
				}
				info.Ram = ram
			}
			requirements := NewInstanceType(info, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil).Requirements

			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpIn, tc.category)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceFamily, corev1.NodeSelectorOpIn, tc.family)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpIn, tc.generation)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceCPU, corev1.NodeSelectorOpIn, tc.cpu)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceMemory, corev1.NodeSelectorOpIn, tc.memory)
			if tc.size == "" {
				assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceSize, corev1.NodeSelectorOpDoesNotExist)
			} else {
				assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceSize, corev1.NodeSelectorOpIn, tc.size)
			}
		})
	}
}

func TestNewInstanceType_RecognizesAllHuaweiMainSeries(t *testing.T) {
	mainSeries := []string{"t", "s", "x", "c", "h", "d", "i", "ir", "m", "e", "p", "g", "pi", "fp", "ai"}
	for _, series := range mainSeries {
		t.Run(series, func(t *testing.T) {
			requirements := NewInstanceType(ecsMdl.Flavor{
				Name:  series + "1.large.1",
				Vcpus: "2",
				Ram:   4096,
			}, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil).Requirements
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpIn, series)
		})
	}
}

func TestNewInstanceType_DerivedHuaweiRequirementsFailIndependently(t *testing.T) {
	testCases := []struct {
		name       string
		flavor     ecsMdl.Flavor
		category   string
		family     string
		generation string
		cpu        string
		memory     string
		size       string
	}{
		{
			name:   "unknown category preserves family and generation",
			flavor: ecsMdl.Flavor{Name: "zc12e.large.2", Vcpus: "2", Ram: 4096},
			family: "zc12e", generation: "12", cpu: "2", memory: "4096", size: "large",
		},
		{
			name:     "invalid size preserves first segment attributes",
			flavor:   ecsMdl.Flavor{Name: "c7.01xlarge.2", Vcpus: "2", Ram: 4096},
			category: "c", family: "c7", generation: "7", cpu: "2", memory: "4096",
		},
		{
			name:     "missing size preserves first segment attributes",
			flavor:   ecsMdl.Flavor{Name: "kx1", Vcpus: "4", Ram: 8192},
			category: "x", family: "kx1", generation: "1", cpu: "4", memory: "8192",
		},
		{
			name:   "malformed generation does not remove structured values",
			flavor: ecsMdl.Flavor{Name: "c7x2.large.2", Vcpus: "2", Ram: 4096},
			cpu:    "2", memory: "4096", size: "large",
		},
		{
			name:   "uppercase generation is rejected independently",
			flavor: ecsMdl.Flavor{Name: "C7.large.2", Vcpus: "2", Ram: 4096},
			cpu:    "2", memory: "4096", size: "large",
		},
		{
			name:   "leading zero generation is rejected independently",
			flavor: ecsMdl.Flavor{Name: "c07.large.2", Vcpus: "2", Ram: 4096},
			cpu:    "2", memory: "4096", size: "large",
		},
		{
			name:       "family label over the Kubernetes limit is rejected independently",
			flavor:     ecsMdl.Flavor{Name: strings.Repeat("z", 64) + "7.large.2", Vcpus: "2", Ram: 4096},
			generation: "7", cpu: "2", memory: "4096", size: "large",
		},
		{
			name:     "invalid cpu does not remove other values",
			flavor:   ecsMdl.Flavor{Name: "c7.large.2", Vcpus: "02", Ram: 4096},
			category: "c", family: "c7", generation: "7", memory: "4096", size: "large",
		},
		{
			name:     "invalid memory does not remove other values",
			flavor:   ecsMdl.Flavor{Name: "c7.large.2", Vcpus: "2", Ram: 0},
			category: "c", family: "c7", generation: "7", cpu: "2", size: "large",
		},
		{
			name:   "aws category alias is not introduced",
			flavor: ecsMdl.Flavor{Name: "r7.large.2", Vcpus: "2", Ram: 4096},
			family: "r7", generation: "7", cpu: "2", memory: "4096", size: "large",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			requirements := NewInstanceType(tc.flavor, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil).Requirements
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceCategory, requirementOperator(tc.category), tc.category)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceFamily, requirementOperator(tc.family), tc.family)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceGeneration, requirementOperator(tc.generation), tc.generation)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceCPU, requirementOperator(tc.cpu), tc.cpu)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceMemory, requirementOperator(tc.memory), tc.memory)
			assertHuaweiRequirement(t, requirements, v1alpha1.LabelInstanceSize, requirementOperator(tc.size), tc.size)
		})
	}
}

func TestNewInstanceType_HuaweiRequirementsUseKarpenterIntersection(t *testing.T) {
	requirements := NewInstanceType(ecsMdl.Flavor{
		Name:  "c7.large.2",
		Vcpus: "2",
		Ram:   4096,
	}, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil).Requirements

	category := requirements.Get(v1alpha1.LabelInstanceCategory)
	if !category.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpIn, "c")) {
		t.Fatalf("expected category In intersection")
	}
	if category.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpIn, "m")) {
		t.Fatalf("expected category In intersection to reject m")
	}
	if !category.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpExists)) {
		t.Fatalf("expected category Exists intersection")
	}
	if category.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpDoesNotExist)) {
		t.Fatalf("expected category DoesNotExist intersection to reject known category")
	}
	generation := requirements.Get(v1alpha1.LabelInstanceGeneration)
	if !generation.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpIn, "7")) {
		t.Fatalf("expected generation In intersection")
	}
	if generation.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpIn, "8")) {
		t.Fatalf("expected generation In intersection to reject generation 8")
	}
	if !generation.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpGt, "6")) {
		t.Fatalf("expected generation Gt intersection")
	}
	if generation.HasIntersection(scheduling.NewRequirement(v1alpha1.LabelInstanceGeneration, corev1.NodeSelectorOpLt, "7")) {
		t.Fatalf("expected generation Lt intersection to reject generation 7")
	}

	unknown := NewInstanceType(ecsMdl.Flavor{Name: "zc12e.large.2", Vcpus: "2", Ram: 4096}, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil).Requirements
	if err := unknown.Compatible(scheduling.NewRequirements(scheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpNotIn, "c")), scheduling.AllowUndefinedWellKnownLabels); err != nil {
		t.Fatalf("expected NotIn to retain missing-label semantics")
	}
	if err := unknown.Compatible(scheduling.NewRequirements(scheduling.NewRequirement(v1alpha1.LabelInstanceCategory, corev1.NodeSelectorOpIn, "c")), scheduling.AllowUndefinedWellKnownLabels); err == nil {
		t.Fatalf("expected In to require a known category")
	}
}

func requirementOperator(value string) corev1.NodeSelectorOperator {
	if value == "" {
		return corev1.NodeSelectorOpDoesNotExist
	}
	return corev1.NodeSelectorOpIn
}

func assertHuaweiRequirement(t *testing.T, requirements scheduling.Requirements, key string, operator corev1.NodeSelectorOperator, values ...string) {
	t.Helper()
	if operator == corev1.NodeSelectorOpDoesNotExist {
		values = nil
	}
	requirement := requirements.Get(key)
	if requirement == nil {
		t.Fatalf("expected requirement for %q", key)
	}
	if got := requirement.Operator(); got != operator {
		t.Fatalf("expected %q operator %q, got %q", key, operator, got)
	}
	if len(values) == 0 {
		if got := requirement.Values(); len(got) != 0 {
			t.Fatalf("expected %q to have no values, got %v", key, got)
		}
		return
	}
	got := requirement.Values()
	if len(got) != len(values) || got[0] != values[0] {
		t.Fatalf("expected %q values %v, got %v", key, values, got)
	}
}

func blockDeviceMappingsWithK8SVolume(size int32) v1alpha1.BlockDeviceMappings {
	return v1alpha1.BlockDeviceMappings{
		K8S: &v1alpha1.BlockDevice{
			VolumeSize: size,
			VolumeType: "SAS",
		},
	}
}

func TestDefaultMaxPodsByMemoryMiB(t *testing.T) {
	testCases := []struct {
		name      string
		memoryMiB int64
		want      int64
	}{
		{name: "4Gi", memoryMiB: 4096, want: 20},
		{name: "8Gi", memoryMiB: 8192, want: 40},
		{name: "16Gi", memoryMiB: 16384, want: 60},
		{name: "32Gi", memoryMiB: 32768, want: 80},
		{name: "64Gi", memoryMiB: 65536, want: 110},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultMaxPodsByMemoryMiB(tc.memoryMiB); got != tc.want {
				t.Fatalf("expected max pods %d, got %d", tc.want, got)
			}
		})
	}
}

func TestDefaultMaxPods_CapsBySupplementaryNICs(t *testing.T) {
	testCases := []struct {
		name   string
		flavor ecsMdl.Flavor
		want   int64
	}{
		{
			name: "c9.large.2-like",
			flavor: ecsMdl.Flavor{
				Ram: 4096,
				OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
					QuotasubNetworkInterfaceMaxNum: stringPtr("16"),
				},
			},
			want: 16,
		},
		{
			name: "c9.xlarge.2-like",
			flavor: ecsMdl.Flavor{
				Ram: 8192,
				OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
					QuotasubNetworkInterfaceMaxNum: stringPtr("32"),
				},
			},
			want: 32,
		},
		{
			name: "ac7.2xlarge.1-like",
			flavor: ecsMdl.Flavor{
				Ram: 16384,
				OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
					QuotasubNetworkInterfaceMaxNum: stringPtr("40"),
				},
			},
			want: 40,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultMaxPods(tc.flavor); got != tc.want {
				t.Fatalf("expected max pods %d, got %d", tc.want, got)
			}
		})
	}
}

func TestDefaultMaxPods_FallsBackToMemoryWhenNICCapMissing(t *testing.T) {
	flavor := ecsMdl.Flavor{Ram: 16384}
	if got := defaultMaxPods(flavor); got != 60 {
		t.Fatalf("expected max pods 60, got %d", got)
	}
}

func TestNewInstanceType_UsesCCEMemoryReservationModel(t *testing.T) {
	flavor := ecsMdl.Flavor{
		Name:  "c6.large.2",
		Ram:   8192,
		Vcpus: "2",
	}

	containerd := NewInstanceType(flavor, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil)
	assertQuantityEqual(t, containerd.Overhead.SystemReserved[corev1.ResourceMemory], "600Mi")
	assertQuantityEqual(t, containerd.Overhead.KubeReserved[corev1.ResourceMemory], "700Mi")
	assertQuantityEqual(t, containerd.Overhead.EvictionThreshold[corev1.ResourceMemory], "100Mi")
	assertQuantityEqual(t, containerd.Allocatable()[corev1.ResourceMemory], "6792Mi")

	docker := NewInstanceType(flavor, "cn-north-4", nil, "docker", nil, v1alpha1.BlockDeviceMappings{}, nil, nil)
	assertQuantityEqual(t, docker.Overhead.SystemReserved[corev1.ResourceMemory], "600Mi")
	assertQuantityEqual(t, docker.Overhead.KubeReserved[corev1.ResourceMemory], "1300Mi")
	assertQuantityEqual(t, docker.Allocatable()[corev1.ResourceMemory], "6192Mi")
}

func TestNewInstanceType_KubeReservedUsesMemoryTierDefaultPodCount(t *testing.T) {
	flavor := ecsMdl.Flavor{
		Name:  "c9.large.2",
		Ram:   4096,
		Vcpus: "2",
		OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
			QuotasubNetworkInterfaceMaxNum: stringPtr("16"),
		},
	}

	containerd := NewInstanceType(flavor, "cn-north-4", nil, "containerd", nil, v1alpha1.BlockDeviceMappings{}, nil, nil)
	assertQuantityEqual(t, containerd.Capacity[corev1.ResourcePods], "16")
	assertQuantityEqual(t, containerd.Overhead.KubeReserved[corev1.ResourceMemory], "600Mi")
	assertQuantityEqual(t, containerd.Allocatable()[corev1.ResourceMemory], "2896Mi")

	docker := NewInstanceType(flavor, "cn-north-4", nil, "docker", nil, v1alpha1.BlockDeviceMappings{}, nil, nil)
	assertQuantityEqual(t, docker.Capacity[corev1.ResourcePods], "16")
	assertQuantityEqual(t, docker.Overhead.KubeReserved[corev1.ResourceMemory], "900Mi")
	assertQuantityEqual(t, docker.Allocatable()[corev1.ResourceMemory], "2596Mi")
}

func TestNewInstanceType_UsesK8SDataVolumeForEphemeralStorage(t *testing.T) {
	flavor := ecsMdl.Flavor{
		Name:  "c6.large.2",
		Ram:   8192,
		Vcpus: "2",
	}

	testCases := []struct {
		name            string
		mappings        v1alpha1.BlockDeviceMappings
		wantCapacity    string
		wantEviction    string
		wantAllocatable string
	}{
		{name: "default 100Gi", mappings: v1alpha1.BlockDeviceMappings{}, wantCapacity: "10214676Ki", wantEviction: "1045982837", wantAllocatable: "9413845387"},
		{name: "120Gi", mappings: blockDeviceMappingsWithK8SVolume(120), wantCapacity: "12274824Ki", wantEviction: "1256941996", wantAllocatable: "11312477780"},
		{name: "150Gi", mappings: blockDeviceMappingsWithK8SVolume(150), wantCapacity: "15371208Ki", wantEviction: "1574011722", wantAllocatable: "14166105270"},
		{name: "190Gi", mappings: blockDeviceMappingsWithK8SVolume(190), wantCapacity: "19430032Ki", wantEviction: "1989635306", wantAllocatable: "17906717462"},
		{name: "230Gi", mappings: blockDeviceMappingsWithK8SVolume(230), wantCapacity: "23558544Ki", wantEviction: "2412394941", wantAllocatable: "21711554115"},
		{name: "320Gi", mappings: blockDeviceMappingsWithK8SVolume(320), wantCapacity: "32843536Ki", wantEviction: "3363178136", wantAllocatable: "30268602728"},
		{name: "450Gi", mappings: blockDeviceMappingsWithK8SVolume(450), wantCapacity: "46125908Ki", wantEviction: "4723293049", wantAllocatable: "42509636743"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			it := NewInstanceType(flavor, "cn-north-4", nil, "containerd", nil, tc.mappings, nil, nil)
			assertQuantityEqual(t, it.Capacity[corev1.ResourceEphemeralStorage], tc.wantCapacity)
			assertQuantityEqual(t, it.Overhead.EvictionThreshold[corev1.ResourceEphemeralStorage], tc.wantEviction)
			assertQuantityEqual(t, it.Allocatable()[corev1.ResourceEphemeralStorage], tc.wantAllocatable)
		})
	}

	defaultDisk := NewInstanceType(flavor, "cn-north-4", nil, "containerd", nil, testCases[0].mappings, nil, nil)
	if _, ok := defaultDisk.Overhead.KubeReserved[corev1.ResourceEphemeralStorage]; ok {
		t.Fatalf("expected default kubeReserved to omit ephemeral-storage")
	}
}

func TestNewInstanceType_UsesConfiguredEphemeralStorageReservations(t *testing.T) {
	flavor := ecsMdl.Flavor{
		Name:  "c6.large.2",
		Ram:   8192,
		Vcpus: "2",
	}

	it := NewInstanceType(
		flavor,
		"cn-north-4",
		nil,
		"containerd",
		nil,
		v1alpha1.BlockDeviceMappings{},
		map[string]string{string(corev1.ResourceEphemeralStorage): "2Gi"},
		map[string]string{string(corev1.ResourceEphemeralStorage): "1Gi"},
	)

	assertQuantityEqual(t, it.Overhead.KubeReserved[corev1.ResourceEphemeralStorage], "2Gi")
	assertQuantityEqual(t, it.Overhead.SystemReserved[corev1.ResourceEphemeralStorage], "1Gi")
	assertQuantityEqual(t, it.Overhead.EvictionThreshold[corev1.ResourceEphemeralStorage], "1045982837")
}

func TestNewInstanceType_UsesExplicitMaxPods(t *testing.T) {
	flavor := ecsMdl.Flavor{
		Name:  "c9.large.2",
		Ram:   16384,
		Vcpus: "2",
	}
	maxPods := int32(48)

	it := NewInstanceType(flavor, "cn-north-4", nil, "containerd", &maxPods, v1alpha1.BlockDeviceMappings{}, nil, nil)
	assertQuantityEqual(t, it.Capacity[corev1.ResourcePods], "48")
}

func TestDefaultResolverCacheKeyIncludesRuntimeType(t *testing.T) {
	resolver := NewDefaultResolver("cn-north-4")

	defaulted := fakeNodeClass{}
	explicitContainerd := fakeNodeClass{
		runtime: &v1alpha1.RuntimeConfiguration{Type: "containerd"},
	}
	docker := fakeNodeClass{
		runtime: &v1alpha1.RuntimeConfiguration{Type: "docker"},
	}

	if got, want := resolver.CacheKey(defaulted), resolver.CacheKey(explicitContainerd); got != want {
		t.Fatalf("expected default runtime cache key %q to match explicit containerd %q", got, want)
	}
	if resolver.CacheKey(explicitContainerd) == resolver.CacheKey(docker) {
		t.Fatalf("expected runtime type to affect cache key")
	}
}

func TestDefaultResolverCacheKeyIncludesBlockDeviceMappings(t *testing.T) {
	resolver := NewDefaultResolver("cn-north-4")

	defaultDisk := fakeNodeClass{}
	customDisk := fakeNodeClass{blockDeviceMappings: blockDeviceMappingsWithK8SVolume(120)}

	if resolver.CacheKey(defaultDisk) == resolver.CacheKey(customDisk) {
		t.Fatalf("expected block device mappings to affect cache key")
	}
}

func TestNormalizedRuntimeType(t *testing.T) {
	testCases := []struct {
		name  string
		input *v1alpha1.RuntimeConfiguration
		want  string
	}{
		{name: "nil defaults to containerd", input: nil, want: "containerd"},
		{name: "empty defaults to containerd", input: &v1alpha1.RuntimeConfiguration{}, want: "containerd"},
		{name: "docker", input: &v1alpha1.RuntimeConfiguration{Type: "docker"}, want: "docker"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizedRuntimeType(tc.input); got != tc.want {
				t.Fatalf("expected runtime type %q, got %q", tc.want, got)
			}
		})
	}
}

func assertQuantityEqual(t *testing.T, got resource.Quantity, want string) {
	t.Helper()
	expected := resource.MustParse(want)
	if got.Cmp(expected) != 0 {
		t.Fatalf("expected quantity %s, got %s", expected.String(), got.String())
	}
}
