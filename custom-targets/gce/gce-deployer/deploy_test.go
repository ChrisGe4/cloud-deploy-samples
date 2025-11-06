// Copyright 2024 Google LLC

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may not use this file except in compliance with the License.
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
	"os"
	"path/filepath"
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/protobuf/proto"
)

type mockGceClient struct {
	getMIG               func(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error)
	getURLMap            func(ctx context.Context, params *params) (*computepb.UrlMap, error)
	getBackendService    func(ctx context.Context, params *backendServiceParams) (*computepb.BackendService, error)
	updateBackendService func(ctx context.Context, params *backendServiceParams, bs *computepb.BackendService) error
	createMIG            func(ctx context.Context, params *migParams, igm *computepb.InstanceGroupManager) error
	deleteMIG            func(ctx context.Context, params *migParams, migName string) error
	patchURLMap          func(ctx context.Context, params *params, urlMap *computepb.UrlMap) error
}

func (m *mockGceClient) GetMIG(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error) {
	return m.getMIG(ctx, params, migName)
}
func (m *mockGceClient) GetURLMap(ctx context.Context, params *params) (*computepb.UrlMap, error) {
	return m.getURLMap(ctx, params)
}
func (m *mockGceClient) GetBackendService(ctx context.Context, params *backendServiceParams) (*computepb.BackendService, error) {
	return m.getBackendService(ctx, params)
}
func (m *mockGceClient) UpdateBackendService(ctx context.Context, params *backendServiceParams, bs *computepb.BackendService) error {
	return m.updateBackendService(ctx, params, bs)
}
func (m *mockGceClient) CreateMIG(ctx context.Context, params *migParams, igm *computepb.InstanceGroupManager) error {
	return m.createMIG(ctx, params, igm)
}
func (m *mockGceClient) DeleteMIG(ctx context.Context, params *migParams, migName string) error {
	return m.deleteMIG(ctx, params, migName)
}
func (m *mockGceClient) PatchURLMap(ctx context.Context, params *params, urlMap *computepb.UrlMap) error {
	return m.patchURLMap(ctx, params, urlMap)
}

func TestGenerateBackendServiceBackends(t *testing.T) {
	fakeMIGName := "test-mig"
	fakeSelfLink := "projects/p/regions/r/instanceGroupManagers/test-mig"
	mockClient := &mockGceClient{
		getMIG: func(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error) {
			return &computepb.InstanceGroupManager{
				Name:     proto.String(fakeMIGName),
				SelfLink: proto.String(fakeSelfLink),
			}, nil
		},
	}
	beTemplate := &computepb.Backend{
		Description: proto.String("test backend"),
	}

	got, err := generateBackendServiceBackends(context.Background(), mockClient, &migParams{}, fakeMIGName, nil, beTemplate)
	if err != nil {
		t.Fatalf("generateBackendServiceBackends() returned error: %v", err)
	}

	want := []*computepb.Backend{
		{
			Description: proto.String("test backend"),
			Group:       proto.String(fakeSelfLink),
		},
	}

	if diff := cmp.Diff(want, got, cmp.Comparer(proto.Equal)); diff != "" {
		t.Errorf("generateBackendServiceBackends() returned diff (-want +got):\n%s", diff)
	}
}

func TestParseManifests(t *testing.T) {
	tests := []struct {
		name       string
		manifest   string
		shouldFail bool
	}{
		{
			name:     "Valid manifests",
			manifest: "\napiVersion: compute.deploy.cloud.google.com/v1\nkind: InstanceGroupManager\nmetadata:\n  name: \"test-mig\"\n---\napiVersion: compute.deploy.cloud.google.com/v1\nkind: BackendService\nmetadata:\n  name: \"test-bs\"",
		},
		{
			name:       "Missing InstanceGroupManager",
			manifest:   "\napiVersion: compute.deploy.cloud.google.com/v1\nkind: BackendService\nmetadata:\n  name: \"test-bs\"",
			shouldFail: true,
		},
		{
			name:       "Malformed manifest",
			manifest:   "{ ",
			shouldFail: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			manifestPath := filepath.Join(tmpDir, "manifest.yaml")
			if err := os.WriteFile(manifestPath, []byte(test.manifest), 0644); err != nil {
				t.Fatalf("failed to write manifest file: %v", err)
			}
			d := &deployer{}
			_, err := d.parseManifests(manifestPath)
			if test.shouldFail {
				if err == nil {
					t.Fatal("expected parsing to fail but it succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsing failed: %v", err)
			}
		})
	}
}

func TestSetRouteWeight(t *testing.T) {
	stableBS := "stable-bs"
	canaryBS := "canary-bs"
	pathMatcher := &computepb.PathMatcher{
		Name: proto.String("test-path-matcher"),
		RouteRules: []*computepb.HttpRouteRule{
			{
				RouteAction: &computepb.HttpRouteAction{
					WeightedBackendServices: []*computepb.WeightedBackendService{
						{
							BackendService: proto.String(stableBS),
							Weight:         proto.Uint32(100),
						},
					},
				},
			},
		},
	}

	t.Run("Set canary weight", func(t *testing.T) {
		if err := setRouteWeightForCanary(pathMatcher, stableBS, canaryBS, 50); err != nil {
			t.Fatalf("setRouteWeightForCanary failed: %v", err)
		}
		if len(pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices) != 2 {
			t.Fatal("expected 2 backend services after setting canary weight")
		}
		if *pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices[0].Weight != 50 {
			t.Errorf("expected stable backend service weight to be 50, got %d", *pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices[0].Weight)
		}
		if *pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices[1].Weight != 50 {
			t.Errorf("expected canary backend service weight to be 50, got %d", *pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices[1].Weight)
		}
	})

	t.Run("Set stable weight", func(t *testing.T) {
		if err := setRouteWeightForStable(pathMatcher, stableBS, canaryBS, 100); err != nil {
			t.Fatalf("setRouteWeightForStable failed: %v", err)
		}
		if len(pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices) != 1 {
			t.Fatal("expected 1 backend service after setting stable weight")
		}
		if *pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices[0].Weight != 100 {
			t.Errorf("expected stable backend service weight to be 100, got %d", *pathMatcher.RouteRules[0].RouteAction.WeightedBackendServices[0].Weight)
		}
	})
}
