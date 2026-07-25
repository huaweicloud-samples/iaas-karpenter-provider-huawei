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

package huawei

import (
	"sync"

	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/auth/basic"
	"github.com/huaweicloud/huaweicloud-sdk-go-v3/core/config"
	coreRegion "github.com/huaweicloud/huaweicloud-sdk-go-v3/core/region"
	ecs "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2"
	ecsMdl "github.com/huaweicloud/huaweicloud-sdk-go-v3/services/ecs/v2/model"
)

// NewECSService returns a ECSAPI that defers building the Huawei ECS client until the first request.
// This avoids calling Huawei IAM APIs (e.g. auto ProjectId discovery) during controller startup.
func NewECSService(region *coreRegion.Region, credential *basic.Credentials, httpConfig *config.HttpConfig) ECSAPI {
	if httpConfig == nil {
		httpConfig = config.DefaultHttpConfig()
	}
	return &ecsService{
		region:     region,
		credential: credential,
		httpConfig: httpConfig,
	}
}

type ecsService struct {
	mu         sync.Mutex
	client     *ecs.EcsClient
	region     *coreRegion.Region
	credential *basic.Credentials
	httpConfig *config.HttpConfig
}

func (s *ecsService) getClient() (*ecs.EcsClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		return s.client, nil
	}

	hcClient, err := ecs.EcsClientBuilder().
		WithRegion(s.region).
		WithCredential(s.credential).
		WithHttpConfig(s.httpConfig).
		SafeBuild()
	if err != nil {
		return nil, err
	}
	s.client = ecs.NewEcsClient(hcClient)
	return s.client, nil
}

func (s *ecsService) CreateServers(request *ecsMdl.CreateServersRequest) (*ecsMdl.CreateServersResponse, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	return client.CreateServers(request)
}

func (s *ecsService) DeleteServers(request *ecsMdl.DeleteServersRequest) (*ecsMdl.DeleteServersResponse, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	return client.DeleteServers(request)
}

func (s *ecsService) ListServersDetails(request *ecsMdl.ListServersDetailsRequest) (*ecsMdl.ListServersDetailsResponse, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	return client.ListServersDetails(request)
}

func (s *ecsService) ListFlavors(request *ecsMdl.ListFlavorsRequest) (*ecsMdl.ListFlavorsResponse, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	return client.ListFlavors(request)
}

func (s *ecsService) ListServerAzInfo(request *ecsMdl.ListServerAzInfoRequest) (*ecsMdl.ListServerAzInfoResponse, error) {
	client, err := s.getClient()
	if err != nil {
		return nil, err
	}
	return client.ListServerAzInfo(request)
}
