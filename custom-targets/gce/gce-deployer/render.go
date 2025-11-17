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
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/custom-targets/util/clouddeploy"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/packages/gcs"
	"gopkg.in/yaml.v3"
)

const (
	// Path to use when downloading the source input archive file.
	srcArchivePath = "/workspace/archive.tgz"
	// Name of the archive uploaded at render time that will be downloaded at deploy time.
	renderedArchiveName = "gce-archive.tgz"
	// Default name for the InstanceGroupManager manifest.
	defaultInstanceGroupManagerManifest = "mig.yaml"
	// Default name for the BackendService manifest.
	defaultBackendServiceManifest = "backend-service.yaml"
	// The maximum length of a GCE resource name.
	maxGceResourceNameLength = 63
	// fromParamCommentPrefix is the comment prefix that indicates that a field's value should be substituted from a deploy parameter.
	fromParamCommentPrefix = "# from-param "
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

	allParams := getAllParams()
	fmt.Println("all params is ", allParams)

	fmt.Println("render request is ", r.req)

	igmPath := r.params.mig.instanceGroupManagerPath
	if igmPath == "" {
		igmPath = defaultInstanceGroupManagerManifest
	}
	fullIgmPath := path.Join(srcPath, igmPath)

	bsPath := r.params.backendService.templatePath
	if bsPath == "" {
		bsPath = defaultBackendServiceManifest
	}
	fullBsPath := path.Join(srcPath, bsPath)
	fmt.Println("bs path is ", fullBsPath)
	hydratedIgmBytes, igm, err := hydrateInstanceGroupManager(fullIgmPath, allParams, r.req.Release, r.params.instanceTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to hydrate InstanceGroupManager manifest: %v", err)
	}
	if r.params.instanceTemplate != "" {
		if err := setNestedField(igm.Content[0], r.params.instanceTemplate, "spec", "instance_template"); err != nil {
			return nil, fmt.Errorf("failed to set instance template in InstanceGroupManager manifest: %v", err)
		}
	}

	allManifests := hydratedIgmBytes
	if _, err := os.Stat(fullBsPath); err == nil {
		fmt.Println("start hydrating the backend service ")
		hydratedBsBytes, err := hydrateBackendService(fullBsPath, allParams)
		if err != nil {
			return nil, fmt.Errorf("failed to hydrate BackendService manifest: %v", err)
		}
		allManifests = append(allManifests, []byte("\n---\n")...)
		allManifests = append(allManifests, hydratedBsBytes...)
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

// hydrateBackendService reads in the BackendService YAML file at the provided path and substitutes values from deploy parameters where specified.
func hydrateBackendService(path string, params map[string]string) ([]byte, error) {
	hydrated, _, err := hydrate(path, params)
	if err != nil {
		return nil, err
	}
	return hydrated, nil
}

func hydrateInstanceGroupManager(path string, params map[string]string, releaseName string, instanceTemplate string) ([]byte, *yaml.Node, error) {
	_, node, err := hydrate(path, params)
	if err != nil {
		return nil, nil, err
	}

	m, err := findMapValue(node.Content[0], "metadata")
	if err != nil {
		return nil, nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing metadata")
	}
	n, err := findMapValue(m, "name")
	if err != nil {
		return nil, nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing metadata.name")
	}
	name := n.Value
	releaseSuffix := releaseName
	if len(releaseName) > 8 {
		releaseSuffix = releaseName[len(releaseName)-8:]
	}
	suffix := "-" + releaseSuffix
	maxBaseNameLength := maxGceResourceNameLength - len(suffix)
	if len(name) > maxBaseNameLength {
		name = name[:maxBaseNameLength]
	}
	n.Value = name + suffix

	// set instance template
	s, err := findMapValue(node.Content[0], "spec")
	if err != nil {
		return nil, nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing spec")
	}
	t, err := findMapValue(s, "instance_template")
	if err != nil {
		return nil, nil, fmt.Errorf("invalid InstanceGroupManager manifest: missing spec.instance_template")
	}
	if len(instanceTemplate) > 0 {
		t.Value = instanceTemplate
	}

	var hydrated bytes.Buffer
	encoder := yaml.NewEncoder(&hydrated)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, nil, fmt.Errorf("failed to marshal yaml: %w", err)
	}
	return hydrated.Bytes(), node, nil
}

// hydrate reads in the YAML file at the provided path and substitutes values from deploy parameters where specified.
// A field in the YAML file can specify that it should be substituted by providing a comment on the same line of the
// form:
//
//	key: value # from-param ${customTarget/paramKey}
//
// The value for the field will be the value of the deploy parameter.
func hydrate(path string, params map[string]string) ([]byte, *yaml.Node, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
	}

	if err := traverse(&root, params); err != nil {
		return nil, nil, fmt.Errorf("failed to traverse yaml node: %w", err)
	}

	var hydrated bytes.Buffer
	encoder := yaml.NewEncoder(&hydrated)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return nil, nil, fmt.Errorf("failed to marshal yaml: %w", err)
	}

	return hydrated.Bytes(), &root, nil
}

// traverse traverses a YAML node, substituting values from deploy parameters where specified.
func traverse(node *yaml.Node, params map[string]string) error {
	if node == nil {
		return nil
	}

	if node.LineComment != "" {
		if strings.HasPrefix(node.LineComment, fromParamCommentPrefix) {
			paramKey := strings.TrimSpace(strings.TrimPrefix(node.LineComment, fromParamCommentPrefix))
			// The deploy parameter format is ${customTarget/key}, remove the surrounding chars.
			paramKey = strings.TrimSuffix(strings.TrimPrefix(paramKey, "${"), "}")
			if val, ok := params[paramKey]; ok {
				node.Value = val
			}
		}
	}

	for _, child := range node.Content {
		if err := traverse(child, params); err != nil {
			return err
		}
	}
	return nil
}

func findMapValue(node *yaml.Node, key string) (*yaml.Node, error) {
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1], nil
		}
	}
	return nil, fmt.Errorf("key %q not found", key)
}

func setNestedField(node *yaml.Node, value string, keys ...string) error {
	for _, key := range keys[:len(keys)-1] {
		var err error
		node, err = findMapValue(node, key)
		if err != nil {
			return err
		}
	}
	lastKey := keys[len(keys)-1]
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == lastKey {
			node.Content[i+1].Value = value
			return nil
		}
	}
	// Key doesn't exist, so add it.
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: lastKey}, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
	return nil
}

func getAllParams() map[string]string {
	params := make(map[string]string)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CLOUD_DEPLOY_") {
			pair := strings.SplitN(e, "=", 2)
			key := strings.TrimPrefix(pair[0], "CLOUD_DEPLOY_")
			key = strings.ReplaceAll(key, "_", "/")
			params[key] = pair[1]
		}
	}
	return params
}
