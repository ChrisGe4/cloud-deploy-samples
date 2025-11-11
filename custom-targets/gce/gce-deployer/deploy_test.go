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
	"net/http"
	"os"
	"path/filepath"
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/custom-targets/util/clouddeploy"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/googleapi"
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

type mockDeployRequest struct {
	clouddeploy.DeployRequest
	downloadInput func(ctx context.Context, gcsClient *storage.Client, objectSuffix, filePath string) (string, error)
	uploadResult  func(ctx context.Context, gcsClient *storage.Client, dr *clouddeploy.DeployResult) (string, error)
	getPercentage func() int
}

func (m *mockDeployRequest) DownloadInput(ctx context.Context, gcsClient *storage.Client, objectSuffix, filePath string) (string, error) {
	return m.downloadInput(ctx, gcsClient, objectSuffix, filePath)
}

func (m *mockDeployRequest) UploadResult(ctx context.Context, gcsClient *storage.Client, dr *clouddeploy.DeployResult) (string, error) {
	return m.uploadResult(ctx, gcsClient, dr)
}

func (m *mockDeployRequest) GetPercentage() int {
	return m.getPercentage()
}

func TestDeploy(t *testing.T) {
	tests := []struct {
		name                   string
		percentage             int
		backendServiceProvided bool
		shouldFail             bool
	}{
		{
			name:                   "Standard deployment - percentage 0",
			percentage:             0,
			backendServiceProvided: false,
			shouldFail:             false,
		},
		{
			name:                   "Canary deployment - percentage 50",
			percentage:             50,
			backendServiceProvided: true,
			shouldFail:             false,
		},
		{
			name:                   "Canary deployment - percentage 50, no backend service name",
			percentage:             50,
			backendServiceProvided: false,
			shouldFail:             true,
		},
		{
			name:                   "Standard deployment - percentage 100",
			percentage:             100,
			backendServiceProvided: true,
			shouldFail:             false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			tmpDir := t.TempDir()
			manifestPath := filepath.Join(tmpDir, "manifest.yaml")
			manifestContent := `
apiVersion: compute.deploy.cloud.google.com/v1
kind: InstanceGroupManager
metadata:
  name: "test-mig"
baseInstanceName: "test-instance"
---
apiVersion: compute.deploy.cloud.google.com/v1
kind: BackendService
metadata:
  name: "test-bs"
port: 80
`

			if err := os.WriteFile(manifestPath, []byte(manifestContent), 0644); err != nil {
				t.Fatalf("failed to write manifest file: %v", err)
			}

			// Mock DeployRequest
			mockReq := &mockDeployRequest{
				DeployRequest: clouddeploy.DeployRequest{
					InputGCSPath:    "gs://test-bucket/input",
					ManifestGCSPath: "gs://test-bucket/manifest.yaml",
					OutputGCSPath:   "gs://test-bucket/output",
				},
				downloadInput: func(ctx context.Context, gcsClient *storage.Client, objectSuffix, filePath string) (string, error) {
					t.Logf("DownloadInput mock received filePath: %s", filePath)
					// Simulate downloading the manifest file
					if objectSuffix == "manifest.yaml" {
						if err := os.WriteFile(filePath, []byte(manifestContent), 0644); err != nil {
							return "", err
						}
					}
					return "gs://test-bucket/test-object", nil
				},
				uploadResult: func(ctx context.Context, gcsClient *storage.Client, dr *clouddeploy.DeployResult) (string, error) {
					return "gs://test-bucket/test-result", nil
				},
				getPercentage: func() int {
					return test.percentage
				},
			}

			// Mock GCE client
			mockGCEClient := &mockGceClient{
				getMIG: func(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error) {
					return nil, &googleapi.Error{Code: http.StatusNotFound} // Simulate not found
				},
				createMIG: func(ctx context.Context, params *migParams, igm *computepb.InstanceGroupManager) error {
					return nil
				},
				getURLMap: func(ctx context.Context, params *params) (*computepb.UrlMap, error) {
					return &computepb.UrlMap{Name: proto.String("test-url-map")}, nil
				},
				getBackendService: func(ctx context.Context, params *backendServiceParams) (*computepb.BackendService, error) {
					return nil, &googleapi.Error{Code: http.StatusNotFound} // Simulate not found
				},
				updateBackendService: func(ctx context.Context, params *backendServiceParams, bs *computepb.BackendService) error {
					return nil
				},
				patchURLMap: func(ctx context.Context, params *params, urlMap *computepb.UrlMap) error {
					return nil
				},
				deleteMIG: func(ctx context.Context, params *migParams, migName string) error {
					return nil
				},
			}

			// Deployer instance
			d := &deployer{
				req: mockReq,
				params: &params{
					backendService: backendServiceParams{
						project: "test-project",
						region:  "test-region",
						name:    "test-bs",
					},
					mig: migParams{
						project: "test-project",
						region:  "test-region",
					},
					cloudLoadBalancerURLMap: "test-url-map",
				},
				gcsClient: &storage.Client{},
				gceClient: mockGCEClient,
				srcPath:   tmpDir,
			}

			// Log the srcPath used by the deployer
			t.Logf("Deployer srcPath: %s", d.srcPath)

			if !test.backendServiceProvided {
				d.params.backendService.name = ""
			}

			_, err := d.deploy(ctx)

			if test.shouldFail {
				if err == nil {
					t.Fatal("expected deploy to fail but it succeeded")
				}
				return
			}

			if err != nil {
				t.Fatalf("deploy failed: %v", err)
			}
		})
	}
}
