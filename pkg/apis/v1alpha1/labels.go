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
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpscheduling "sigs.k8s.io/karpenter/pkg/scheduling"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis"
)

var (
	RestrictedLabelDomains = []string{
		apis.Group,
	}

	LabelInstanceCategory         = apis.Group + "/instance-category"
	LabelInstanceFamily           = apis.Group + "/instance-family"
	LabelInstanceGeneration       = apis.Group + "/instance-generation"
	LabelInstanceSize             = apis.Group + "/instance-size"
	LabelInstanceCPU              = apis.Group + "/instance-cpu"
	LabelInstanceMemory           = apis.Group + "/instance-memory"
	LabelInstanceNetworkBandwidth = apis.Group + "/instance-network-bandwidth"
	LabelInstanceGPUName          = apis.Group + "/instance-gpu-name"
	LabelInstanceGPUManufacturer  = apis.Group + "/instance-gpu-manufacturer"
	LabelInstanceGPUCount         = apis.Group + "/instance-gpu-count"
	LabelInstanceGPUMemory        = apis.Group + "/instance-gpu-memory"

	AnnotationCCENodeClassHash        = apis.Group + "/ccenodeclass-hash"
	AnnotationCCENodeClassHashVersion = apis.Group + "/ccenodeclass-hash-version"

	AnnotationNodeID     = apis.Group + "/cce-node-id"
	AnnotationInstanceID = apis.Group + "/ecs-server-id"
)

func init() {
	karpv1.RestrictedLabelDomains = karpv1.RestrictedLabelDomains.Insert(RestrictedLabelDomains...)
	karpv1.WellKnownLabels = karpv1.WellKnownLabels.Insert(
		LabelInstanceCategory,
		LabelInstanceFamily,
		LabelInstanceGeneration,
		LabelInstanceSize,
		LabelInstanceCPU,
		LabelInstanceMemory,
		LabelInstanceNetworkBandwidth,
		LabelInstanceGPUName,
		LabelInstanceGPUManufacturer,
		LabelInstanceGPUCount,
		LabelInstanceGPUMemory,
		corev1.LabelWindowsBuild,
	)
	// CCE sets node.kubernetes.io/network-unavailable during node initialization.
	// Treat it as a known ephemeral taint so the scheduler does not permanently
	// reject pods onto nodes that are still completing their network setup.
	karpscheduling.KnownEphemeralTaints = append(karpscheduling.KnownEphemeralTaints, corev1.Taint{
		Key:    corev1.TaintNodeNetworkUnavailable,
		Effect: corev1.TaintEffectNoSchedule,
	})
}
