// Copyright 2024 Google LLC

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     https://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/api/googleapi"
)

const (
	operationTimeout  = 10 * time.Minute
	retryInitialDelay = 1 * time.Second
	retryMaxDelay     = 30 * time.Second
	retryFactor       = 2.0
)

var (
	urlMapRegex = regexp.MustCompile("projects/(?P<project>[^/]+)/locations/(?P<location>[^/]+)/urlMaps/(?P<name>.+)")
)

// GceClient provides an interface for interacting with GCE services.
type GceClient struct {
	client                      *compute.InstanceGroupManagersClient
	regionalClient              *compute.RegionInstanceGroupManagersClient
	globalBackendServicesClient *compute.GlobalBackendServicesClient
	regionBackendServicesClient *compute.RegionBackendServicesClient
	globalURLMapsClient         *compute.GlobalUrlMapsClient
	regionURLMapsClient         *compute.RegionUrlMapsClient
}

// NewGceClient creates a new GceClient.
func NewGceClient(ctx context.Context) (*GceClient, error) {
	client, err := compute.NewInstanceGroupManagersRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE service client: %w", err)
	}
	regionalClient, err := compute.NewRegionInstanceGroupManagersRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create regional GCE service client: %w", err)
	}
	globalBackendServicesClient, err := compute.NewGlobalBackendServicesRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE global backend services client: %w", err)
	}
	regionBackendServicesClient, err := compute.NewRegionBackendServicesRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE regional backend services client: %w", err)
	}
	globalURLMapsClient, err := compute.NewGlobalUrlMapsRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE global URL maps client: %w", err)
	}
	regionURLMapsClient, err := compute.NewRegionUrlMapsRESTClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE regional URL maps client: %w", err)
	}
	return &GceClient{
		client:                      client,
		regionalClient:              regionalClient,
		globalBackendServicesClient: globalBackendServicesClient,
		regionBackendServicesClient: regionBackendServicesClient,
		globalURLMapsClient:         globalURLMapsClient,
		regionURLMapsClient:         regionURLMapsClient,
	}, nil
}

// CreateMIG creates a new Managed Instance Group.
func (g *GceClient) CreateMIG(ctx context.Context, params *params, igm *computepb.InstanceGroupManager) error {
	fmt.Printf("Creating Managed Instance Group %s\n", *igm.Name)
	var op *compute.Operation
	var err error
	if params.zone != "" {
		req := &computepb.InsertInstanceGroupManagerRequest{
			Project:                      params.project,
			Zone:                         params.zone,
			InstanceGroupManagerResource: igm,
		}
		op, err = g.client.Insert(ctx, req, retryOptions()...)
	} else {
		req := &computepb.InsertRegionInstanceGroupManagerRequest{
			Project:                      params.project,
			Region:                       params.region,
			InstanceGroupManagerResource: igm,
		}
		op, err = g.regionalClient.Insert(ctx, req, retryOptions()...)
	}

	if err != nil {
		return fmt.Errorf("failed to create Managed Instance Group: %w", err)
	}
	return g.WaitForOperation(ctx, op)
}

// UpdateBackendService creates or updates a Backend Service to point to a new MIG.
func (g *GceClient) UpdateBackendService(ctx context.Context, params *params, bs *computepb.BackendService, igmLink string) error {
	fmt.Printf("Updating Backend Service %s\n", *bs.Name)
	isRegional := params.region != "" && bs.Region != nil
	var getOp *computepb.BackendService
	var getErr error

	if isRegional {
		getOp, getErr = g.regionBackendServicesClient.Get(ctx, &computepb.GetRegionBackendServiceRequest{
			Project:        params.project,
			Region:         params.region,
			BackendService: *bs.Name,
		}, retryOptions()...)
	} else {
		getOp, getErr = g.globalBackendServicesClient.Get(ctx, &computepb.GetGlobalBackendServiceRequest{
			Project:        params.project,
			BackendService: *bs.Name,
		}, retryOptions()...)
	}

	newBackend := &computepb.Backend{Group: &igmLink}

	var op *compute.Operation
	var opErr error

	if getErr != nil {
		var apiErr *googleapi.Error
		if errors.As(getErr, &apiErr) && apiErr.Code == http.StatusNotFound {
			fmt.Printf("Backend service %s not found, creating it.\n", *bs.Name)
			bs.Backends = []*computepb.Backend{newBackend}
			if isRegional {
				op, opErr = g.regionBackendServicesClient.Insert(ctx, &computepb.InsertRegionBackendServiceRequest{
					Project:                params.project,
					Region:                 params.region,
					BackendServiceResource: bs,
				}, retryOptions()...)
			} else {
				op, opErr = g.globalBackendServicesClient.Insert(ctx, &computepb.InsertGlobalBackendServiceRequest{
					Project:                params.project,
					BackendServiceResource: bs,
				}, retryOptions()...)
			}
		} else {
			return fmt.Errorf("failed to get backend service %s: %w", *bs.Name, getErr)
		}
	} else {
		fmt.Printf("Backend service %s found, updating it.\n", *bs.Name)
		if isRegional {
			op, opErr = g.regionBackendServicesClient.Update(ctx, &computepb.UpdateRegionBackendServiceRequest{
				Project:                params.project,
				Region:                 params.region,
				BackendService:         *bs.Name,
				BackendServiceResource: bs,
			}, retryOptions()...)
		} else {
			op, opErr = g.globalBackendServicesClient.Update(ctx, &computepb.UpdateGlobalBackendServiceRequest{
				Project:                params.project,
				BackendService:         *bs.Name,
				BackendServiceResource: bs,
			}, retryOptions()...)
		}
	}

	if opErr != nil {
		return fmt.Errorf("failed to update backend service %s: %w", *bs.Name, opErr)
	}

	return g.WaitForOperation(ctx, op)
}

// GetURLMap retrieves a URL Map.
func (g *GceClient) GetURLMap(ctx context.Context, params *params) (*computepb.UrlMap, error) {
	isRegional, urlMapName, err := parseURLMap(params.cloudLoadBalancerURLMap)
	if err != nil {
		return nil, err
	}

	if isRegional {
		return g.regionURLMapsClient.Get(ctx, &computepb.GetRegionUrlMapRequest{
			Project: params.project,
			Region:  params.region,
			UrlMap:  urlMapName,
		}, retryOptions()...)
	}
	return g.globalURLMapsClient.Get(ctx, &computepb.GetGlobalUrlMapRequest{
		Project: params.project,
		UrlMap:  urlMapName,
	}, retryOptions()...)
}

// PatchURLMap patches a URL Map.
func (g *GceClient) PatchURLMap(ctx context.Context, params *params, urlMap *computepb.UrlMap) error {
	fmt.Printf("Patching URL Map %s\n", params.cloudLoadBalancerURLMap)
	isRegional, urlMapName, err := parseURLMap(params.cloudLoadBalancerURLMap)
	if err != nil {
		return err
	}

	var op *compute.Operation
	if isRegional {
		op, err = g.regionURLMapsClient.Patch(ctx, &computepb.PatchRegionUrlMapRequest{
			Project:        params.project,
			Region:         params.region,
			UrlMap:         urlMapName,
			UrlMapResource: urlMap,
		}, retryOptions()...)
	} else {
		op, err = g.globalURLMapsClient.Patch(ctx, &computepb.PatchGlobalUrlMapRequest{
			Project:        params.project,
			UrlMap:         urlMapName,
			UrlMapResource: urlMap,
		}, retryOptions()...)
	}

	if err != nil {
		return fmt.Errorf("failed to patch URL Map: %w", err)
	}

	return g.WaitForOperation(ctx, op)
}

// DeleteMIG deletes a Managed Instance Group.
func (g *GceClient) DeleteMIG(ctx context.Context, params *params, migName string) error {
	fmt.Printf("Deleting Managed Instance Group %s\n", migName)
	var err error
	if params.zone != "" {
		_, err = g.client.Get(ctx, &computepb.GetInstanceGroupManagerRequest{
			Project:              params.project,
			Zone:                 params.zone,
			InstanceGroupManager: migName,
		}, retryOptions()...)
	} else {
		_, err = g.regionalClient.Get(ctx, &computepb.GetRegionInstanceGroupManagerRequest{
			Project:              params.project,
			Region:               params.region,
			InstanceGroupManager: migName,
		}, retryOptions()...)
	}

	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			fmt.Printf("Managed Instance Group %s not found, skipping deletion.\n", migName)
			return nil
		}
		return fmt.Errorf("failed to get Managed Instance Group %s: %w", migName, err)
	}

	var op *compute.Operation
	if params.zone != "" {
		req := &computepb.DeleteInstanceGroupManagerRequest{
			Project:              params.project,
			Zone:                 params.zone,
			InstanceGroupManager: migName,
		}
		op, err = g.client.Delete(ctx, req, retryOptions()...)
	} else {
		req := &computepb.DeleteRegionInstanceGroupManagerRequest{
			Project:              params.project,
			Region:               params.region,
			InstanceGroupManager: migName,
		}
		op, err = g.regionalClient.Delete(ctx, req, retryOptions()...)
	}

	if err != nil {
		return fmt.Errorf("failed to delete Managed Instance Group %s: %w", migName, err)
	}
	return g.WaitForOperation(ctx, op)
}

// WaitForOperation waits for a GCE operation to complete.
func (g *GceClient) WaitForOperation(ctx context.Context, op *compute.Operation) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()

	err := op.Wait(ctx)
	if err != nil {
		return fmt.Errorf("failed to wait for operation: %w", err)
	}

	fmt.Println("Operation completed successfully")
	return nil
}

func retryOptions() []gax.CallOption {
	return []gax.CallOption{
		gax.WithTimeout(600000 * time.Millisecond),
		gax.WithRetry(func() gax.Retryer {
			return gax.OnHTTPCodes(gax.Backoff{
				Initial:    retryInitialDelay,
				Max:        retryMaxDelay,
				Multiplier: retryFactor,
			},
				http.StatusGatewayTimeout,
				http.StatusServiceUnavailable,
				http.StatusTooManyRequests,
				http.StatusInternalServerError,
			)
		}),
	}
}

func parseURLMap(urlMap string) (bool, string, error) {
	match := urlMapRegex.FindStringSubmatch(urlMap)
	if match == nil {
		return false, "", fmt.Errorf("invalid URL map format: %s", urlMap)
	}

	result := make(map[string]string)
	for i, name := range urlMapRegex.SubexpNames() {
		if i != 0 && name != "" {
			result[name] = match[i]
		}
	}

	location, okL := result["location"]
	name, okN := result["name"]

	if !okL || !okN {
		return false, "", fmt.Errorf("invalid URL map format, missing location or name: %s", urlMap)
	}

	return location != "global", name, nil
}
