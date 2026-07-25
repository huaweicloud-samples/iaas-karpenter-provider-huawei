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

package operator

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/global"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/provider"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/config"
	coreRegion "github.com/huaweicloud/huaweicloud-sdk-go-v3/core/region"
	bssRegion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/bss/v2/region"
	cceRegion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/cce/v3/region"
	ecsRegion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/region"
	vpcRegion "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/vpc/v2/region"
	"github.com/patrickmn/go-cache"
	"github.com/samber/lo"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/karpenter/pkg/operator"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instance"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/instancetype"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/pricing"

	sdk "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/huawei"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/subnet"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/providers/version"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/utils"
)

const (
	// DefaultTTL is the default cache TTL for Huawei Cloud API calls used during
	// setup verification and provisioning. Cache hits enable faster provisioning
	// and reduced API load, which can have a serious impact on performance and
	// scalability. DO NOT CHANGE THIS VALUE WITHOUT DUE CONSIDERATION.
	DefaultTTL = time.Minute
	// DefaultCleanupInterval triggers cache cleanup (lazy eviction) at this interval.
	DefaultCleanupInterval = time.Minute
	// AvailableIPAddressTTL is time to drop AvailableIPAddress data if it is not updated within the TTL
	AvailableIPAddressTTL = 5 * time.Minute
	// InstanceTypesZonesAndOfferingsTTL is the time before we refresh instance types, zones, and offerings
	InstanceTypesZonesAndOfferingsTTL = 5 * time.Minute
	// UnavailableOfferingTTL is the duration to suppress recently sold-out offerings from scheduling.
	UnavailableOfferingTTL = 5 * time.Minute
	// if it is not updated by a node creation event or refreshed during controller reconciliation
	DiscoveredCapacityCacheTTL = 60 * 24 * time.Hour
	BillingEndpoint            = "https://bss.myhuaweicloud.com"
	BillingEndpointEnv         = "HUAWEICLOUD_BSS_ENDPOINT"
	IgnoreSSLEnv               = "HUAWEICLOUD_SDK_IGNORE_SSL_VERIFICATION"
)

// Operator is injected into the HuaweiCloud CloudProvider's factories
type Operator struct {
	*operator.Operator
	VersionProvider      *version.DefaultProvider
	SubnetProvider       subnet.Provider
	InstanceTypeProvider *instancetype.DefaultProvider
	InstanceProvider     instance.Provider
	PricingProvider      *pricing.DefaultProvider
}

func NewOperator(ctx context.Context, operator *operator.Operator) (context.Context, *Operator) {
	logger := log.FromContext(ctx)

	reg := os.Getenv("HUAWEICLOUD_SDK_REGION_ID")
	vpcReg, err := vpcRegion.SafeValueOf(reg)
	if err != nil {
		lo.Must0(fmt.Errorf("unable to get VPC region: %w", err))
	}
	ecsReg, err := ecsRegion.SafeValueOf(reg)
	if err != nil {
		lo.Must0(fmt.Errorf("unable to get ECS region: %w", err))
	}
	cceReg, err := cceRegion.SafeValueOf(reg)
	if err != nil {
		lo.Must0(fmt.Errorf("unable to get CCE region: %w", err))
	}
	cred, err := provider.BasicCredentialProviderChain().GetCredentials()
	if err != nil {
		lo.Must0(fmt.Errorf("unable to get basic credentials: %w", err))
	}
	credentials, ok := cred.(*basic.Credentials)
	lo.Must0(ok, "unexpected basic credentials type: %T", cred)

	globalCred, err := provider.GlobalCredentialProviderChain().GetCredentials()
	if err != nil {
		lo.Must0(fmt.Errorf("unable to get global credentials: %w", err))
	}
	globalCredentials, ok := globalCred.(*global.Credentials)
	lo.Must0(ok, "unexpected global credentials type: %T", globalCred)

	clusterID := os.Getenv("HUAWEICLOUD_SDK_CCE_CLUSTER_ID")
	if clusterID == "" {
		lo.Must0(fmt.Errorf("missing required env: HUAWEICLOUD_SDK_CCE_CLUSTER_ID"))
	}

	httpConfig, err := sdkHTTPConfig()
	if err != nil {
		lo.Must0(fmt.Errorf("unable to build SDK HTTP config: %w", err))
	}

	vpcApi := sdk.NewVPCService(vpcReg, credentials, httpConfig)
	subnetProvider := subnet.NewDefaultProvider(vpcApi, cache.New(DefaultTTL, DefaultCleanupInterval), cache.New(AvailableIPAddressTTL, DefaultCleanupInterval))

	versionProvider := version.NewDefaultProvider(operator.KubernetesInterface)
	lo.Must0(versionProvider.UpdateVersionWithValidation(ctx))

	ecsApi := sdk.NewECSService(ecsReg, credentials, httpConfig)
	bssApi := sdk.NewBSSService(billingRegion(reg), globalCredentials, httpConfig)
	cceApi := sdk.NewCCEService(cceReg, credentials, httpConfig)
	unavailableOfferingCache := utils.NewOfferingAvailabilityCache(UnavailableOfferingTTL, DefaultCleanupInterval)
	instanceProvider := instance.NewDefaultProvider(clusterID, cceApi, subnetProvider, unavailableOfferingCache)
	pricingProvider := pricing.NewDefaultProvider(bssApi, reg, func() string { return credentials.ProjectId })
	instanceTypeProvider := instancetype.NewDefaultProvider(
		ecsApi,
		cache.New(InstanceTypesZonesAndOfferingsTTL, DefaultCleanupInterval),
		cache.New(DiscoveredCapacityCacheTTL, DefaultCleanupInterval),
		unavailableOfferingCache,
		instancetype.NewDefaultResolver(reg),
		pricingProvider.OnDemandPrice,
	)

	if err := instanceTypeProvider.Refresh(ctx); err != nil {
		logger.Error(err, "failed to preload instance types and offerings")
	}
	if err := pricingProvider.UpdateOnDemandPricing(ctx, instanceTypeProvider.InstanceTypeInfos()); err != nil {
		logger.Error(err, "failed to preload on-demand pricing", "region", reg, "bss-endpoint", billingRegionEndpoint(billingRegion(reg)))
	}
	return ctx, &Operator{
		Operator:             operator,
		VersionProvider:      versionProvider,
		SubnetProvider:       subnetProvider,
		InstanceTypeProvider: instanceTypeProvider,
		InstanceProvider:     instanceProvider,
		PricingProvider:      pricingProvider,
	}
}

func sdkHTTPConfig() (*config.HttpConfig, error) {
	httpConfig := config.DefaultHttpConfig()

	raw := strings.TrimSpace(os.Getenv(IgnoreSSLEnv))
	if raw == "" {
		return httpConfig, nil
	}

	ignoreSSLVerification, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", IgnoreSSLEnv, err)
	}
	return httpConfig.WithIgnoreSSLVerification(ignoreSSLVerification), nil
}

func billingRegion(regionID string) *coreRegion.Region {
	if endpoint := strings.TrimSpace(os.Getenv(BillingEndpointEnv)); endpoint != "" {
		return coreRegion.NewRegion(regionID, endpoint)
	}

	reg, err := bssRegion.SafeValueOf(regionID)
	if err == nil && reg != nil {
		return reg
	}
	return coreRegion.NewRegion(regionID, BillingEndpoint)
}

func billingRegionEndpoint(region *coreRegion.Region) string {
	if region == nil || len(region.Endpoints) == 0 {
		return ""
	}
	return region.Endpoints[0]
}
