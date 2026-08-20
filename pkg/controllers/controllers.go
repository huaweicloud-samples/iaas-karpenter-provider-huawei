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

package controllers

import (
	"context"

	"github.com/awslabs/operatorpkg/controller"
	"github.com/awslabs/operatorpkg/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/events"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instancetype"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/pricing"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/nodeclaim/unregisteredtaint"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/nodeclass"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/subnet"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/version"

	nodeclaimcapacity "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/nodeclaim/capacity"
	nodeclasshash "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/nodeclass/hash"
	controllersinstancetype "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/providers/instancetype"
	controllerspricing "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/providers/pricing"
	controllersversion "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/controllers/providers/version"
)

// NewControllers returns the list of controllers managed by this provider.
func NewControllers(
	ctx context.Context,
	mgr manager.Manager,
	kubeClient client.Client,
	recorder events.Recorder,
	_ cloudprovider.CloudProvider,
	versionProvider *version.DefaultProvider,
	subnetProvider subnet.Provider,
	instanceTypeProvider *instancetype.DefaultProvider,
	pricingProvider *pricing.DefaultProvider,
) []controller.Controller {
	controllers := []controller.Controller{
		controllersversion.NewController(versionProvider, versionProvider.UpdateVersionWithValidation),
		status.NewController[*v1alpha1.CCENodeClass](kubeClient, mgr.GetEventRecorderFor("karpenter"), status.EmitDeprecatedMetrics), //nolint:staticcheck // SA1019: operatorpkg/status still requires record.EventRecorder
		nodeclass.NewController(kubeClient, recorder, subnetProvider),
		controllersinstancetype.NewController(instanceTypeProvider),
		controllerspricing.NewController(pricingProvider, instanceTypeProvider),
		unregisteredtaint.NewController(kubeClient),
		nodeclaimcapacity.NewController(kubeClient, instanceTypeProvider),
		nodeclasshash.NewController(kubeClient),
	}
	return controllers
}
