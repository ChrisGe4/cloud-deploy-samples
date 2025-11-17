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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/cloud-deploy-samples/packages/gcs"
	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
)

const (
	testIgmManifest = `
apiVersion: compute.deploy.cloud.google.com/v1
kind: InstanceGroupManager
metadata:
  name: "test-mig" # from-param ${customTarget/instanceGroupManager}
spec:
  instance_template: "test-template" # from-param ${customTarget/instanceTemplate}
  update_policy:
    minimal_action: "CREATE"  # from-param ${customTarget/gceAction}
`
	testBsManifest = `
apiVersion: compute.deploy.cloud.google.com/v1
kind: BackendService
metadata:
  name: "test-bs" # from-param ${customTarget/backendService}
`
)

func TestHydrate(t *testing.T) {
	tests := []struct {
		name       string
		manifest   string
		params     map[string]string
		expected   map[string]interface{}
		shouldFail bool
	}{
		{
			name:     "Successful hydration",
			manifest: testIgmManifest,
			params: map[string]string{
				"customTarget/instanceGroupManager": "hydrated-mig",
				"customTarget/instanceTemplate":     "hydrated-template",
				"customTarget/gceAction":            "REPLACE",
			},
			expected: map[string]interface{}{
				"apiVersion": "compute.deploy.cloud.google.com/v1",
				"kind":       "InstanceGroupManager",
				"metadata": map[string]interface{}{
					"name": "hydrated-mig",
				},
				"spec": map[string]interface{}{
					"instance_template": "hydrated-template",
					"update_policy": map[string]interface{}{
						"minimal_action": "REPLACE",
					},
				},
			},
		}, {
			name:     "Missing param",
			manifest: testIgmManifest,
			params:   map[string]string{},
			expected: map[string]interface{}{
				"apiVersion": "compute.deploy.cloud.google.com/v1",
				"kind":       "InstanceGroupManager",
				"metadata": map[string]interface{}{
					"name": "test-mig",
				},
				"spec": map[string]interface{}{
					"instance_template": "test-template",
					"update_policy": map[string]interface{}{
						"minimal_action": "CREATE",
					},
				},
			},
		},
		{
			name:       "Malformed manifest",
			manifest:   "{",
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

			hydrated, _, err := hydrate(manifestPath, test.params)
			if test.shouldFail {
				if err == nil {
					t.Fatal("expected hydration to fail but it succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("hydration failed: %v", err)
			}

			var actual map[string]interface{}
			if err := yaml.Unmarshal(hydrated, &actual); err != nil {
				t.Fatalf("failed to unmarshal hydrated manifest: %v", err)
			}

			if diff := cmp.Diff(test.expected, actual, mapComparer); diff != "" {
				t.Errorf("hydrated manifest differs from expected (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHydrateInstanceGroupManager(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "manifest.yaml")
	if err := os.WriteFile(manifestPath, []byte(testIgmManifest), 0644); err != nil {
		t.Fatalf("failed to write manifest file: %v", err)
	}

	releaseName := "test-release-12345678"
	hydrated, _, err := hydrateInstanceGroupManager(manifestPath, map[string]string{}, releaseName)
	if err != nil {
		t.Fatalf("hydration failed: %v", err)
	}

	var actual map[string]interface{}
	if err := yaml.Unmarshal(hydrated, &actual); err != nil {
		t.Fatalf("failed to unmarshal hydrated manifest: %v", err)
	}

	metadata, ok := actual["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("missing metadata in hydrated manifest")
	}
	name, ok := metadata["name"].(string)
	if !ok {
		t.Fatal("missing name in hydrated manifest metadata")
	}

	expectedSuffix := "-12345678"
	if !strings.HasSuffix(name, expectedSuffix) {
		t.Errorf("expected name to have suffix %q but it was %q", expectedSuffix, name)
	}
}

type mockGCSUploader struct {
	uploads map[string][]byte
}

func (m *mockGCSUploader) Upload(ctx context.Context, object string, content *gcs.UploadContent) (string, error) {
	if m.uploads == nil {
		m.uploads = make(map[string][]byte)
	}
	m.uploads[object] = content.Data
	return "gcs://" + object, nil
}

func TestHydrateBackendService(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "backend-service.yaml")
	if err := os.WriteFile(manifestPath, []byte(testBsManifest), 0644); err != nil {
		t.Fatalf("failed to write manifest file: %v", err)
	}

	params := map[string]string{
		"customTarget/backendService": "hydrated-bs",
	}

	hydrated, err := hydrateBackendService(manifestPath, params)
	if err != nil {
		t.Fatalf("hydration failed: %v", err)
	}

	var actual map[string]interface{}
	if err := yaml.Unmarshal(hydrated, &actual); err != nil {
		t.Fatalf("failed to unmarshal hydrated manifest: %v", err)
	}

	expected := map[string]interface{}{
		"apiVersion": "compute.deploy.cloud.google.com/v1",
		"kind":       "BackendService",
		"metadata": map[string]interface{}{
			"name": "hydrated-bs",
		},
	}

	if diff := cmp.Diff(expected, actual, mapComparer); diff != "" {
		t.Errorf("hydrated manifest differs from expected (-want +got):\n%s", diff)
	}
}

var mapComparer = cmp.Comparer(func(x, y map[string]interface{}) bool {
	if len(x) != len(y) {
		return false
	}
	for k, v := range x {
		if w, ok := y[k]; !ok || !cmp.Equal(v, w) {
			return false
		}
	}
	return true
})
