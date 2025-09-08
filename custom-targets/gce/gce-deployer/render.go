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
	"fmt"
	"os"
	"path"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/custom-targets/util/clouddeploy"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/packages/gcs"
	"gopkg.in/yaml.v2"
)

const (
	// Path to use when downloading the source input archive file.
	srcArchivePath = "/workspace/archive.tgz"
	// Path to use when unarchiving the source input.
	srcPath = "/workspace/source"
	// Name of the archive uploaded at render time that will be downloaded at deploy time.
	renderedArchiveName = "gce-archive.tgz"
	// Default name for the InstanceGroupManager manifest.
	defaultInstanceGroupManagerManifest = "mig.yaml"
	// Default name for the BackendService manifest.
	defaultBackendServiceManifest = "backend-service.yaml"
	// The maximum length of a GCE resource name.
	maxGceResourceNameLength = 63
)

// renderer implements the requestHandler interface for render requests.
type renderer struct {
	req       *clouddeploy.RenderRequest
	params    *params
	gcsClient *storage.Client
}

// process processes a render request and uploads succeeded or failed results to GCS for Cloud Deploy.
func (r *renderer) process(ctx context.Context) error {
	fmt.Println("Processing render request")

	res, err := r.render(ctx)
	if err != nil {
		fmt.Printf("Render failed: %v\n", err)
		rr := &clouddeploy.RenderResult{
			ResultStatus:   clouddeploy.RenderFailed,
			FailureMessage: err.Error(),
			Metadata: map[string]string{
				clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
				clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
			},
		}
		fmt.Println("Uploading failed render results")
		rURI, err := r.req.UploadResult(ctx, r.gcsClient, rr)
		if err != nil {
			return fmt.Errorf("error uploading failed render results: %v", err)
		}
		fmt.Printf("Uploaded failed render results to %s\n", rURI)
		return err
	}

	fmt.Println("Uploading render results")
	rURI, err := r.req.UploadResult(ctx, r.gcsClient, res)
	if err != nil {
		return fmt.Errorf("error uploading render results: %v", err)
	}
	fmt.Printf("Uploaded render results to %s\n", rURI)
	return nil
}

// render performs the following steps:
//  1. Hydrate the manifests with values from the deploy parameters.
//  2. Upload the hydrated manifests to GCS to use as the Cloud Deploy Release inspector artifact.
//  3. Upload the archived source configuration to GCS so it can be used at deploy time.
//
// Returns either the render results or an error if the render failed.
func (r *renderer) render(ctx context.Context) (*clouddeploy.RenderResult, error) {
	fmt.Printf("Downloading render input archive to %s and unarchiving to %s\n", srcArchivePath, srcPath)
	inURI, err := r.req.DownloadAndUnarchiveInput(ctx, r.gcsClient, srcArchivePath, srcPath)
	if err != nil {
		return nil, fmt.Errorf("unable to download and unarchive render input: %v", err)
	}
	fmt.Printf("Downloaded render input archive from %s\n", inURI)

	igmPath := r.params.instanceGroupManagerPath
	if igmPath == "" {
		igmPath = defaultInstanceGroupManagerManifest
	}
	fullIgmPath := path.Join(srcPath, igmPath)

	bsPath := r.params.backendServiceTemplatePath
	if bsPath == "" {
		bsPath = defaultBackendServiceManifest
	}
	fullBsPath := path.Join(srcPath, bsPath)

	hydratedIgm, err := r.hydrateInstanceGroupManager(fullIgmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate InstanceGroupManager manifest: %v", err)
	}

	allManifests := hydratedIgm
	if _, err := os.Stat(fullBsPath); err == nil {
		bsContent, err := os.ReadFile(fullBsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read BackendService manifest: %v", err)
		}
		allManifests = append(allManifests, []byte("\n---\n")...)
		allManifests = append(allManifests, bsContent...)
	}

	fmt.Println("Uploading hydrated manifests")
	mURI, err := r.req.UploadArtifact(ctx, r.gcsClient, "manifest.yaml", &gcs.UploadContent{Data: allManifests})
	if err != nil {
		return nil, fmt.Errorf("error uploading manifest: %v", err)
	}
	fmt.Printf("Uploaded hydrated manifests to %s\n", mURI)

	fmt.Println("Uploading archived source configuration for use at deploy time")
	ahURI, err := r.req.UploadArtifact(ctx, r.gcsClient, renderedArchiveName, &gcs.UploadContent{LocalPath: srcArchivePath})
	if err != nil {
		return nil, fmt.Errorf("error uploading archived source configuration: %v", err)
	}
	fmt.Printf("Uploaded archived source configuration to %s\n", ahURI)

	rr := &clouddeploy.RenderResult{
		ResultStatus: clouddeploy.RenderSucceeded,
		ManifestFile: mURI,
		Metadata: map[string]string{
			clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
			clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
		},
	}
	return rr, nil
}

func (r *renderer) hydrateInstanceGroupManager(path string) ([]byte, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var igm map[string]interface{}
	if err := yaml.Unmarshal(content, &igm); err != nil {
		return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	metadata, ok := igm["metadata"].(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing metadata")
	}
	name, ok := metadata["name"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing metadata.name")
	}
	releaseName := r.req.Release
	suffix := "-" + releaseName[len(releaseName)-8:]
	maxBaseNameLength := maxGceResourceNameLength - len(suffix)
	if len(name) > maxBaseNameLength {
		name = name[:maxBaseNameLength]
	}
	metadata["name"] = name + suffix

	if r.params.instanceTemplate != "" {
		spec, ok := igm["spec"].(map[interface{}]interface{})
		if !ok {
			return nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing spec")
		}
		spec["instance_template"] = r.params.instanceTemplate
	}

	hydrated, err := yaml.Marshal(igm)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal yaml: %w", err)
	}
	return hydrated, nil
}
