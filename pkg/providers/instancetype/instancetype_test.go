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
	"math"
	"testing"
	"time"

	ecsMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/model"
	"github.com/patrickmn/go-cache"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"

	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/apis/v1alpha1"
	sdk "github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/huawei"
	"github.com/HuaweiCloudDeveloper/karpenter-provider-huawei/pkg/utils"
)

func TestFetchInstanceTypes_PaginatesListFlavors(t *testing.T) {
	firstPage := make([]ecsMdl.Flavor, listFlavorsPageSize)
	for i := range firstPage {
		id := fmt.Sprintf("flavor-%04d", i)
		firstPage[i] = ecsMdl.Flavor{Id: id, Name: id}
	}
	secondPage := []ecsMdl.Flavor{
		{Id: "flavor-1000", Name: "flavor-1000"},
		{Id: "flavor-1001", Name: "flavor-1001"},
	}

	fakeAPI := &fakeECSAPI{
		listFlavorsFunc: func(request *ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
			if request.Limit == nil || *request.Limit != listFlavorsPageSize {
				t.Fatalf("expected list flavors limit %d, got %+v", listFlavorsPageSize, request.Limit)
			}
			switch {
			case request.Marker == nil:
				return &ecsMdl.ListFlavorsResponse{Flavors: &firstPage}, nil
			case *request.Marker == firstPage[len(firstPage)-1].Id:
				return &ecsMdl.ListFlavorsResponse{Flavors: &secondPage}, nil
			default:
				t.Fatalf("unexpected marker %q", *request.Marker)
				return nil, nil
			}
		},
	}

	p := NewDefaultProvider(fakeAPI, nil, nil, nil, nil, func(sdk.InstanceType) (float64, bool) {
		return 0, false
	})

	flavors, err := p.fetchInstanceTypes()
	if err != nil {
		t.Fatalf("expected no error fetching instance types, got %v", err)
	}
	if len(flavors) != len(firstPage)+len(secondPage) {
		t.Fatalf("expected %d flavors, got %d", len(firstPage)+len(secondPage), len(flavors))
	}
	if fakeAPI.listFlavorsCalls != 2 {
		t.Fatalf("expected 2 list flavors calls, got %d", fakeAPI.listFlavorsCalls)
	}
	if len(fakeAPI.listFlavorsRequests) != 2 {
		t.Fatalf("expected 2 captured requests, got %d", len(fakeAPI.listFlavorsRequests))
	}
	if fakeAPI.listFlavorsRequests[0].Marker != nil {
		t.Fatalf("expected first request marker to be nil, got %q", *fakeAPI.listFlavorsRequests[0].Marker)
	}
	if fakeAPI.listFlavorsRequests[1].Marker == nil || *fakeAPI.listFlavorsRequests[1].Marker != firstPage[len(firstPage)-1].Id {
		t.Fatalf("expected second request marker %q, got %+v", firstPage[len(firstPage)-1].Id, fakeAPI.listFlavorsRequests[1].Marker)
	}
}

func TestUpdateInstanceTypes_RefreshesFlavorsOnRepeatedCalls(t *testing.T) {
	call := 0
	fakeAPI := &fakeECSAPI{
		listFlavorsFunc: func(request *ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
			if request.Marker != nil {
				t.Fatalf("expected marker to be nil for single-page refresh, got %q", *request.Marker)
			}
			var flavors []ecsMdl.Flavor
			switch call {
			case 0:
				flavors = []ecsMdl.Flavor{{Id: "flavor-a", Name: "c6.large.2"}}
			case 1:
				flavors = []ecsMdl.Flavor{{Id: "flavor-b", Name: "c7.large.2"}}
			default:
				t.Fatalf("unexpected refresh call %d", call+1)
			}
			call++
			return &ecsMdl.ListFlavorsResponse{Flavors: &flavors}, nil
		},
	}

	p := NewDefaultProvider(
		fakeAPI,
		cache.New(time.Minute, time.Minute),
		cache.New(time.Minute, time.Minute),
		nil,
		nil,
		func(sdk.InstanceType) (float64, bool) { return 0, false },
	)

	if err := p.UpdateInstanceTypes(context.Background()); err != nil {
		t.Fatalf("expected first update to succeed, got %v", err)
	}
	if infos := p.InstanceTypeInfos(); len(infos) != 1 || infos["c6.large.2"].Id != "flavor-a" {
		t.Fatalf("expected first refresh to load c6.large.2/flavor-a, got %+v", infos)
	}

	if err := p.UpdateInstanceTypes(context.Background()); err != nil {
		t.Fatalf("expected second update to succeed, got %v", err)
	}
	if infos := p.InstanceTypeInfos(); len(infos) != 1 || infos["c7.large.2"].Id != "flavor-b" {
		t.Fatalf("expected second refresh to replace cache with c7.large.2/flavor-b, got %+v", infos)
	}
	if fakeAPI.listFlavorsCalls != 2 {
		t.Fatalf("expected repeated updates to re-fetch flavors, got %d calls", fakeAPI.listFlavorsCalls)
	}
}

func TestUpdateInstanceTypeOfferings_RefreshesLatestFlavorAvailability(t *testing.T) {
	call := 0
	fakeAPI := &fakeECSAPI{
		listFlavorsFunc: func(request *ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
			if request.Marker != nil {
				t.Fatalf("expected marker to be nil for single-page refresh, got %q", *request.Marker)
			}
			var flavors []ecsMdl.Flavor
			switch call {
			case 0:
				flavors = []ecsMdl.Flavor{{
					Id:   "flavor-a",
					Name: "c6.large.2",
					OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
						Condoperationstatus: stringPtr("normal"),
						Condoperationaz:     stringPtr("cn-north-4a(normal)"),
					},
				}}
			case 1:
				flavors = []ecsMdl.Flavor{{
					Id:   "flavor-a",
					Name: "c6.large.2",
					OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
						Condoperationstatus: stringPtr("normal"),
						Condoperationaz:     stringPtr("cn-north-4a(sellout)"),
					},
				}}
			default:
				t.Fatalf("unexpected refresh call %d", call+1)
			}
			call++
			return &ecsMdl.ListFlavorsResponse{Flavors: &flavors}, nil
		},
	}

	p := NewDefaultProvider(
		fakeAPI,
		cache.New(time.Minute, time.Minute),
		cache.New(time.Minute, time.Minute),
		nil,
		nil,
		func(sdk.InstanceType) (float64, bool) { return 0, false },
	)

	if err := p.UpdateInstanceTypeOfferings(context.Background()); err != nil {
		t.Fatalf("expected first offering refresh to succeed, got %v", err)
	}
	if zones := p.instanceTypesOfferings["c6.large.2"]; zones.Len() != 1 || !zones.Has("cn-north-4a") {
		t.Fatalf("expected cn-north-4a to be available after first refresh, got %v", zones.UnsortedList())
	}

	if err := p.UpdateInstanceTypeOfferings(context.Background()); err != nil {
		t.Fatalf("expected second offering refresh to succeed, got %v", err)
	}
	if zones := p.instanceTypesOfferings["c6.large.2"]; zones.Len() != 0 {
		t.Fatalf("expected cn-north-4a to be removed after sellout refresh, got %v", zones.UnsortedList())
	}
	if fakeAPI.listFlavorsCalls != 2 {
		t.Fatalf("expected repeated offering updates to re-fetch flavors, got %d calls", fakeAPI.listFlavorsCalls)
	}
}

func TestRefresh_UsesSingleFetchPerRefreshAndUpdatesTypesAndOfferings(t *testing.T) {
	call := 0
	fakeAPI := &fakeECSAPI{
		listFlavorsFunc: func(request *ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
			if request.Marker != nil {
				t.Fatalf("expected marker to be nil for single-page refresh, got %q", *request.Marker)
			}
			var flavors []ecsMdl.Flavor
			switch call {
			case 0:
				flavors = []ecsMdl.Flavor{{
					Id:   "flavor-a",
					Name: "c6.large.2",
					OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
						Condoperationstatus: stringPtr("normal"),
						Condoperationaz:     stringPtr("cn-north-4a(normal)"),
					},
				}}
			case 1:
				flavors = []ecsMdl.Flavor{{
					Id:   "flavor-b",
					Name: "c7.large.2",
					OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
						Condoperationstatus: stringPtr("normal"),
						Condoperationaz:     stringPtr("cn-north-4b(normal)"),
					},
				}}
			default:
				t.Fatalf("unexpected refresh call %d", call+1)
			}
			call++
			return &ecsMdl.ListFlavorsResponse{Flavors: &flavors}, nil
		},
	}

	p := NewDefaultProvider(
		fakeAPI,
		cache.New(time.Minute, time.Minute),
		cache.New(time.Minute, time.Minute),
		nil,
		nil,
		func(sdk.InstanceType) (float64, bool) { return 0, false },
	)

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("expected first refresh to succeed, got %v", err)
	}
	if fakeAPI.listFlavorsCalls != 1 {
		t.Fatalf("expected first refresh to fetch flavors once, got %d calls", fakeAPI.listFlavorsCalls)
	}
	if infos := p.InstanceTypeInfos(); len(infos) != 1 || infos["c6.large.2"].Id != "flavor-a" {
		t.Fatalf("expected first refresh to load c6.large.2/flavor-a, got %+v", infos)
	}
	if zones := p.instanceTypesOfferings["c6.large.2"]; zones.Len() != 1 || !zones.Has("cn-north-4a") {
		t.Fatalf("expected first refresh to expose cn-north-4a, got %v", zones.UnsortedList())
	}

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("expected second refresh to succeed, got %v", err)
	}
	if fakeAPI.listFlavorsCalls != 2 {
		t.Fatalf("expected second refresh to fetch flavors once more, got %d calls", fakeAPI.listFlavorsCalls)
	}
	if infos := p.InstanceTypeInfos(); len(infos) != 1 || infos["c7.large.2"].Id != "flavor-b" {
		t.Fatalf("expected second refresh to replace cache with c7.large.2/flavor-b, got %+v", infos)
	}
	if zones := p.instanceTypesOfferings["c7.large.2"]; zones.Len() != 1 || !zones.Has("cn-north-4b") {
		t.Fatalf("expected second refresh to expose cn-north-4b, got %v", zones.UnsortedList())
	}
}

func TestListUsesFlavorOfferingsWithoutSubnetZones(t *testing.T) {
	flavors := []ecsMdl.Flavor{{
		Id:    "flavor-a",
		Name:  "c6.large.2",
		Ram:   4096,
		Vcpus: "2",
		OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
			Condoperationstatus: stringPtr("normal"),
			Condoperationaz:     stringPtr("cn-north-4a(normal)"),
		},
	}}
	p := NewDefaultProvider(
		&fakeECSAPI{listFlavorsFunc: func(*ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
			return &ecsMdl.ListFlavorsResponse{Flavors: &flavors}, nil
		}},
		cache.New(time.Minute, time.Minute),
		cache.New(time.Minute, time.Minute),
		nil,
		NewDefaultResolver("cn-north-4"),
		func(sdk.InstanceType) (float64, bool) { return 0.42, true },
	)

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("refreshing instance types: %v", err)
	}
	instanceTypes, err := p.List(context.Background(), fakeNodeClass{})
	if err != nil {
		t.Fatalf("listing instance types without subnet zones: %v", err)
	}
	if len(instanceTypes) != 1 || len(instanceTypes[0].Offerings) != 1 {
		t.Fatalf("expected one instance type with one offering, got %#v", instanceTypes)
	}
	zone := instanceTypes[0].Offerings[0].Requirements.Get(corev1.LabelTopologyZone)
	if !zone.Has("cn-north-4a") {
		t.Fatalf("expected offering in cn-north-4a, got %v", zone.Values())
	}
}

func TestListUsesECSAvailabilityZonesWhenFlavorHasNoAZOverrides(t *testing.T) {
	flavors := []ecsMdl.Flavor{{
		Id:    "flavor-a",
		Name:  "c6.large.2",
		Ram:   4096,
		Vcpus: "2",
		OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
			Condoperationstatus: stringPtr("normal"),
		},
	}}
	p := NewDefaultProvider(
		&fakeECSAPI{
			listFlavorsFunc: func(*ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
				return &ecsMdl.ListFlavorsResponse{Flavors: &flavors}, nil
			},
			listAvailabilityZonesFunc: func(*ecsMdl.ListServerAzInfoRequest) (*ecsMdl.ListServerAzInfoResponse, error) {
				return &ecsMdl.ListServerAzInfoResponse{AvailabilityZones: &[]ecsMdl.ListServerAzInfo{{
					AvailabilityZoneId: "cn-north-4a",
				}}}, nil
			},
		},
		cache.New(time.Minute, time.Minute),
		cache.New(time.Minute, time.Minute),
		nil,
		NewDefaultResolver("cn-north-4"),
		func(sdk.InstanceType) (float64, bool) { return 0.42, true },
	)

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("refreshing instance types: %v", err)
	}
	instanceTypes, err := p.List(context.Background(), fakeNodeClass{})
	if err != nil {
		t.Fatalf("listing instance types: %v", err)
	}
	if len(instanceTypes) != 1 || len(instanceTypes[0].Offerings) != 1 {
		t.Fatalf("expected one instance type with one offering, got %#v", instanceTypes)
	}
	zone := instanceTypes[0].Offerings[0].Requirements.Get(corev1.LabelTopologyZone)
	if !zone.Has("cn-north-4a") {
		t.Fatalf("expected offering in cn-north-4a, got %v", zone.Values())
	}
}

func TestListDoesNotCreateOfferingsForGloballyUnavailableFlavor(t *testing.T) {
	flavors := []ecsMdl.Flavor{
		{
			Id:    "available-flavor",
			Name:  "c6.large.2",
			Ram:   4096,
			Vcpus: "2",
			OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
				Condoperationstatus: stringPtr("normal"),
			},
		},
		{
			Id:    "unavailable-flavor",
			Name:  "c7.large.2",
			Ram:   4096,
			Vcpus: "2",
			OsExtraSpecs: &ecsMdl.FlavorExtraSpec{
				Condoperationstatus: stringPtr("sellout"),
			},
		},
	}
	p := NewDefaultProvider(
		&fakeECSAPI{
			listFlavorsFunc: func(*ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
				return &ecsMdl.ListFlavorsResponse{Flavors: &flavors}, nil
			},
			listAvailabilityZonesFunc: func(*ecsMdl.ListServerAzInfoRequest) (*ecsMdl.ListServerAzInfoResponse, error) {
				return &ecsMdl.ListServerAzInfoResponse{AvailabilityZones: &[]ecsMdl.ListServerAzInfo{{AvailabilityZoneId: "cn-north-4a"}}}, nil
			},
		},
		cache.New(time.Minute, time.Minute),
		cache.New(time.Minute, time.Minute),
		nil,
		NewDefaultResolver("cn-north-4"),
		func(sdk.InstanceType) (float64, bool) { return 0.42, true },
	)

	if err := p.Refresh(context.Background()); err != nil {
		t.Fatalf("refreshing instance types: %v", err)
	}
	instanceTypes, err := p.List(context.Background(), fakeNodeClass{})
	if err != nil {
		t.Fatalf("listing instance types: %v", err)
	}
	offeringCounts := map[string]int{}
	for _, instanceType := range instanceTypes {
		offeringCounts[instanceType.Name] = len(instanceType.Offerings)
	}
	if offeringCounts["c6.large.2"] != 1 {
		t.Fatalf("expected available flavor to have one offering, got %#v", offeringCounts)
	}
	if offeringCounts["c7.large.2"] != 0 {
		t.Fatalf("expected globally unavailable flavor to have no offerings, got %#v", offeringCounts)
	}
}

func TestParseCondOperationAZ(t *testing.T) {
	got := parseCondOperationAZ("cn-south-2b(normal), cn-south-1c(sellout),cn-south-1e(obt)，cn-south-1f(promotion); cn-south-1g(abandon)")

	assertMapEntry(t, got, "cn-south-2b", "normal")
	assertMapEntry(t, got, "cn-south-1c", "sellout")
	assertMapEntry(t, got, "cn-south-1e", "obt")
	assertMapEntry(t, got, "cn-south-1f", "promotion")
	assertMapEntry(t, got, "cn-south-1g", "abandon")
}

func TestCondOperationStatusAvailableUsesCanonicalCase(t *testing.T) {
	if !condOperationStatusAvailable("ABANDON") {
		t.Fatalf("expected non-canonical status to use the permissive default")
	}
}

func TestCondOperationStatusAvailableRejectsOBTSellout(t *testing.T) {
	if condOperationStatusAvailable("obt_sellout") {
		t.Fatalf("expected OBT sellout status to be unavailable")
	}
}

func TestResolveOfferingZones_DefaultAbandon_WithOverrides(t *testing.T) {
	universe := sets.New[string]("cn-south-2b", "cn-south-1c", "cn-south-1e", "cn-south-1f", "cn-south-1g")
	extra := &ecsMdl.FlavorExtraSpec{
		Condoperationstatus: stringPtr("abandon"),
		Condoperationaz:     stringPtr("cn-south-2b(normal), cn-south-1c(sellout), cn-south-1e(obt), cn-south-1f(promotion)"),
	}

	zones := resolveOfferingZones(universe, extra)
	assertZones(t, zones, "cn-south-2b", "cn-south-1e", "cn-south-1f")
}

func TestResolveOfferingZones_DefaultNormal_WithSelloutException(t *testing.T) {
	universe := sets.New[string]("cn-south-2b", "cn-south-1c", "cn-south-1e")
	extra := &ecsMdl.FlavorExtraSpec{
		Condoperationstatus: stringPtr("normal"),
		Condoperationaz:     stringPtr("cn-south-1c(sellout)"),
	}

	zones := resolveOfferingZones(universe, extra)
	assertZones(t, zones, "cn-south-2b", "cn-south-1e")
}

func TestResolveOfferingZones_DefaultSellout_WithNormalOverride(t *testing.T) {
	universe := sets.New[string]("cn-south-1c", "cn-south-1e")
	extra := &ecsMdl.FlavorExtraSpec{
		Condoperationstatus: stringPtr("sellout"),
		Condoperationaz:     stringPtr("cn-south-1e(normal)"),
	}

	zones := resolveOfferingZones(universe, extra)
	assertZones(t, zones, "cn-south-1e")
}

func TestComputeRequirementsLeavesZonesUnsetWhenOfferingsAreEmpty(t *testing.T) {
	flavor := ecsMdl.Flavor{
		Name:  "c3.large",
		Ram:   8192,
		Vcpus: "2",
	}

	reqs := computeRequirements(flavor, "cn-south-1", nil)
	gotZones := sets.New(reqs.Get(corev1.LabelTopologyZone).Values()...)
	if gotZones.Len() != 0 {
		t.Fatalf("expected no zones without offering data, got %v", gotZones.UnsortedList())
	}

	gotRegions := sets.New(reqs.Get(corev1.LabelTopologyRegion).Values()...)
	if gotRegions.Len() != 1 || !gotRegions.Has("cn-south-1") {
		t.Fatalf("expected region {cn-south-1}, got %v", gotRegions.UnsortedList())
	}

	gotOS := sets.New(reqs.Get(corev1.LabelOSStable).Values()...)
	if gotOS.Len() != 1 || !gotOS.Has(string(corev1.Linux)) {
		t.Fatalf("expected os {linux}, got %v", gotOS.UnsortedList())
	}
}

func TestCreateOfferings_InjectsOnDemandPrice(t *testing.T) {
	p := NewDefaultProvider(nil, nil, nil, nil, nil, func(instanceType sdk.InstanceType) (float64, bool) {
		if instanceType != "c6.large.2" {
			return 0, false
		}
		return 0.42, true
	})
	p.instanceTypesOfferings = map[sdk.InstanceType]sets.Set[string]{
		"c6.large.2": sets.New[string]("cn-north-4a"),
	}

	offerings := p.createOfferings(context.Background(), &cloudprovider.InstanceType{
		Name: "c6.large.2",
		Requirements: computeRequirements(ecsMdl.Flavor{
			Name:  "c6.large.2",
			Ram:   4096,
			Vcpus: "2",
		}, "cn-north-4", []string{"cn-north-4a"}),
	}, sets.New[string]("cn-north-4a"))

	if len(offerings) != 1 {
		t.Fatalf("expected 1 offering, got %d", len(offerings))
	}
	if offerings[0].Price != 0.42 {
		t.Fatalf("expected price 0.42, got %f", offerings[0].Price)
	}
}

func TestCreateOfferings_UnknownOnDemandPriceUsesMaxFloat(t *testing.T) {
	p := NewDefaultProvider(nil, nil, nil, nil, nil, func(sdk.InstanceType) (float64, bool) {
		return 0, false
	})
	p.instanceTypesOfferings = map[sdk.InstanceType]sets.Set[string]{
		"c6.large.2": sets.New[string]("cn-north-4a"),
	}

	offerings := p.createOfferings(context.Background(), &cloudprovider.InstanceType{
		Name: "c6.large.2",
		Requirements: computeRequirements(ecsMdl.Flavor{
			Name:  "c6.large.2",
			Ram:   4096,
			Vcpus: "2",
		}, "cn-north-4", []string{"cn-north-4a"}),
	}, sets.New[string]("cn-north-4a"))

	if len(offerings) != 1 {
		t.Fatalf("expected 1 offering, got %d", len(offerings))
	}
	if offerings[0].Price != math.MaxFloat64 {
		t.Fatalf("expected price %f, got %f", math.MaxFloat64, offerings[0].Price)
	}
}

func TestCreateOfferings_UsesUnavailableOfferingCache(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(time.Minute, time.Minute)
	availabilityCache.MarkUnavailable(karpv1.CapacityTypeOnDemand, "c6.large.2", "cn-north-4a")

	p := NewDefaultProvider(nil, nil, nil, availabilityCache, nil, func(instanceType sdk.InstanceType) (float64, bool) {
		if instanceType != "c6.large.2" {
			return 0, false
		}
		return 0.42, true
	})
	p.instanceTypesOfferings = map[sdk.InstanceType]sets.Set[string]{
		"c6.large.2": sets.New[string]("cn-north-4a", "cn-north-4b"),
	}

	offerings := p.createOfferings(context.Background(), &cloudprovider.InstanceType{
		Name: "c6.large.2",
		Requirements: computeRequirements(ecsMdl.Flavor{
			Name:  "c6.large.2",
			Ram:   4096,
			Vcpus: "2",
		}, "cn-north-4", []string{"cn-north-4a", "cn-north-4b"}),
	}, sets.New[string]("cn-north-4a", "cn-north-4b"))

	if len(offerings) != 2 {
		t.Fatalf("expected 2 offerings, got %d", len(offerings))
	}

	availabilityByZone := map[string]bool{}
	for _, offering := range offerings {
		zone := offering.Requirements.Get(corev1.LabelTopologyZone).Values()[0]
		availabilityByZone[zone] = offering.Available
	}

	if availabilityByZone["cn-north-4a"] {
		t.Fatalf("expected cn-north-4a offering to be unavailable, got %#v", availabilityByZone)
	}
	if !availabilityByZone["cn-north-4b"] {
		t.Fatalf("expected cn-north-4b offering to remain available, got %#v", availabilityByZone)
	}
}

func TestCreateOfferings_UnavailableOfferingCacheExpires(t *testing.T) {
	availabilityCache := utils.NewOfferingAvailabilityCache(20*time.Millisecond, 5*time.Millisecond)
	availabilityCache.MarkUnavailable(karpv1.CapacityTypeOnDemand, "c6.large.2", "cn-north-4a")

	p := NewDefaultProvider(nil, nil, nil, availabilityCache, nil, func(instanceType sdk.InstanceType) (float64, bool) {
		if instanceType != "c6.large.2" {
			return 0, false
		}
		return 0.42, true
	})
	p.instanceTypesOfferings = map[sdk.InstanceType]sets.Set[string]{
		"c6.large.2": sets.New[string]("cn-north-4a"),
	}

	before := p.createOfferings(context.Background(), &cloudprovider.InstanceType{
		Name: "c6.large.2",
		Requirements: computeRequirements(ecsMdl.Flavor{
			Name:  "c6.large.2",
			Ram:   4096,
			Vcpus: "2",
		}, "cn-north-4", []string{"cn-north-4a"}),
	}, sets.New[string]("cn-north-4a"))
	if len(before) != 1 || before[0].Available {
		t.Fatalf("expected offering to be unavailable before ttl expiry, got %#v", before)
	}

	time.Sleep(50 * time.Millisecond)

	after := p.createOfferings(context.Background(), &cloudprovider.InstanceType{
		Name: "c6.large.2",
		Requirements: computeRequirements(ecsMdl.Flavor{
			Name:  "c6.large.2",
			Ram:   4096,
			Vcpus: "2",
		}, "cn-north-4", []string{"cn-north-4a"}),
	}, sets.New[string]("cn-north-4a"))
	if len(after) != 1 || !after[0].Available {
		t.Fatalf("expected offering to recover after ttl expiry, got %#v", after)
	}
}

type fakeNodeClass struct {
	kubelet             *v1alpha1.KubeletConfiguration
	runtime             *v1alpha1.RuntimeConfiguration
	blockDeviceMappings v1alpha1.BlockDeviceMappings
}

type fakeECSAPI struct {
	listFlavorsFunc           func(*ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error)
	listAvailabilityZonesFunc func(*ecsMdl.ListServerAzInfoRequest) (*ecsMdl.ListServerAzInfoResponse, error)
	listFlavorsCalls          int
	listFlavorsRequests       []*ecsMdl.ListFlavorsRequest
}

func (f fakeNodeClass) KubeletConfiguration() *v1alpha1.KubeletConfiguration {
	return f.kubelet
}

func (f fakeNodeClass) RuntimeConfiguration() *v1alpha1.RuntimeConfiguration {
	return f.runtime
}

func (f fakeNodeClass) BlockDeviceMappings() v1alpha1.BlockDeviceMappings {
	return f.blockDeviceMappings
}

func (f *fakeECSAPI) ListServersDetails(*ecsMdl.ListServersDetailsRequest) (*ecsMdl.ListServersDetailsResponse, error) {
	return &ecsMdl.ListServersDetailsResponse{}, nil
}

func (f *fakeECSAPI) ListFlavors(request *ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
	f.listFlavorsCalls++
	requestCopy := *request
	f.listFlavorsRequests = append(f.listFlavorsRequests, &requestCopy)
	if f.listFlavorsFunc == nil {
		return &ecsMdl.ListFlavorsResponse{}, nil
	}
	return f.listFlavorsFunc(request)
}

func (f *fakeECSAPI) ListServerAzInfo(request *ecsMdl.ListServerAzInfoRequest) (*ecsMdl.ListServerAzInfoResponse, error) {
	if f.listAvailabilityZonesFunc == nil {
		return &ecsMdl.ListServerAzInfoResponse{}, nil
	}
	return f.listAvailabilityZonesFunc(request)
}

func stringPtr(v string) *string {
	return &v
}

func assertMapEntry(t *testing.T, m map[string]string, key, want string) {
	t.Helper()
	if got, ok := m[key]; !ok || got != want {
		t.Fatalf("expected %q=%q, got %q (present=%v)", key, want, got, ok)
	}
}

func assertZones(t *testing.T, zones sets.Set[string], want ...string) {
	t.Helper()
	wantSet := sets.New[string](want...)
	if zones.Len() != wantSet.Len() || !zones.HasAll(want...) {
		t.Fatalf("expected zones=%v, got %v", wantSet.UnsortedList(), zones.UnsortedList())
	}
}
