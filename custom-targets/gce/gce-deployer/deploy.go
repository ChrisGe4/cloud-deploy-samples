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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/custom-targets/util/clouddeploy"
	"github.com/google/go-cpy/cpy"
	"google.golang.org/api/googleapi"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
	sigsk8s "sigs.k8s.io/yaml"
)

const (
	GLOBAL = "global"
)

// deployRequestInterface provides an interface for mocking the DeployRequest.
type deployRequestInterface interface {
	DownloadInput(ctx context.Context, gcsClient *storage.Client, objectSuffix, localPath string) (string, error)
	UploadResult(ctx context.Context, gcsClient *storage.Client, deployResult *clouddeploy.DeployResult) (string, error)
	GetPercentage() int
}

// deployer implements the requestHandler interface for deploy requests.
type deployer struct {
	req       deployRequestInterface
	params    *params
	gcsClient *storage.Client
	gceClient gceClientInterface
	srcPath   string
}

// process processes a deploy request and uploads succeeded or failed results to GCS for Cloud Deploy.
func (d *deployer) process(ctx context.Context) error {
	fmt.Println("Processing deploy request")
	dr := &clouddeploy.DeployResult{
		ResultStatus: clouddeploy.DeploySucceeded,
		Metadata: map[string]string{
			clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
			clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
		},
	}
	res, err := d.deploy(ctx)
	if err != nil {
		fmt.Printf("Deploy failed: %v\n", err)
		dr = &clouddeploy.DeployResult{
			ResultStatus:   clouddeploy.DeployFailed,
			FailureMessage: err.Error(),
			Metadata: map[string]string{
				clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
				clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
			},
		}
	}
	if res != nil {
		dr = res
	}

	fmt.Println("Uploading deploy results")
	rURI, err := d.req.UploadResult(ctx, d.gcsClient, dr)
	if err != nil {
		return fmt.Errorf("error uploading deploy results: %v", err)
	}
	fmt.Printf("Uploaded deploy results to %s\n", rURI)
	return nil
}

// deploy performs the following steps:
//  1. Download and unarchive the rendered manifests.
//  2. Create or update GCE resources based on the manifests.
//  3. Upload deploy artifacts.
func (d *deployer) deploy(ctx context.Context) (*clouddeploy.DeployResult, error) {
	// fmt.Printf("Downloading rendered configuration archive to %s\n", srcArchivePath)
	// inURI, err := d.req.DownloadInput(ctx, d.gcsClient, renderedArchiveName, srcArchivePath)
	// if err != nil {
	// 	return nil, fmt.Errorf("unable to download deploy input with object suffix %s: %v", renderedArchiveName, err)
	// }
	// fmt.Printf("Downloaded rendered configuration archive from %s\n", inURI)
	//
	// archiveFile, err := os.Open(srcArchivePath)
	// if err != nil {
	// 	return nil, fmt.Errorf("unable to open archive file %s: %v", srcArchivePath, err)
	// }
	// fmt.Printf("Unarchiving rendered configuration in %s to %s\n", srcArchivePath, srcPath)
	// if err := archiver.NewTarGz().Unarchive(archiveFile.Name(), srcPath); err != nil {
	// 	return nil, fmt.Errorf("unable to unarchive rendered configuration: %v", err)
	// }
	manifestFile := "manifest.yaml"
	renderedDeploymentPath := path.Join(d.srcPath, manifestFile)
	fmt.Printf("Downloading rendered Deployment to %s\n", renderedDeploymentPath)
	dURI, err := d.req.DownloadInput(ctx, d.gcsClient, manifestFile, renderedDeploymentPath)
	if err != nil {
		return nil, fmt.Errorf("unable to download rendered deployment with object suffix %s: %v", manifestFile, err)
	}
	fmt.Printf("Downloaded rendered Deployment from %s\n", dURI)

	manifests, err := d.parseManifests(renderedDeploymentPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifests: %w", err)
	}

	beTemplate := manifests.bs.Backends[0]
	stableSvc := d.params.backendService.name
	stableSvcFullName := constructBackendServiceFullName(d.params.backendService.project, d.params.backendService.region, stableSvc)
	canarySvc := stableSvc + "-canary"
	canarySvcFullName := constructBackendServiceFullName(d.params.backendService.project, d.params.backendService.region, canarySvc)
	migName := *manifests.igm.Name
	// igm := manifests.igm
	// if err := d.gceClient.CreateMIG(ctx, &d.params.mig, igm); err != nil {
	// 	return nil, fmt.Errorf("failed to create MIG: %w", err)
	// }
	backendServiceNameProvided := len(stableSvc) != 0
	percentage := d.req.GetPercentage()
	if percentage == 0 || percentage == 100 {
		// Standard deployment or promoting canary to 100%
		// The stable phase consists of the following steps:
		//  1. If it is canary deployment, fetch the MIG in the canary backend service.
		//  2. Upsert the stable backend service to pick up the changes to the backend service template. Also replace the MIG with the canary MIG fetched from step 1 if needed.
		//  3. Create the MIG if it's standard deployment. Then update the backends of the stable backend service with the new MIG.
		//  4. Start redirecting traffic to stable backend service by updating the weights of the stable and canary backend services
		//     in the specified route.
		//  5. Delete the original stable MIG.
		//  For standard deployment, if the backendService name if not provided,
		if !backendServiceNameProvided {
			_, err := d.getOrCreateMIG(ctx, migName, manifests)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}
		urlMap, err := d.gceClient.GetURLMap(ctx, d.params)
		if err != nil {
			return nil, fmt.Errorf("failed to get URL map: %w", err)
		}
		var canaryBackends []*computepb.Backend
		var canaryBackendService *computepb.BackendService
		if isBackendServiceInURLMap(urlMap, canarySvcFullName) {
			canaryBackendService, err = d.gceClient.GetBackendService(ctx, &backendServiceParams{
				project: d.params.backendService.project,
				region:  d.params.backendService.region,
				name:    canarySvc,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to get canary backend service: %w", err)
			}
			canaryBackends = canaryBackendService.GetBackends()
		}
		stableBackendService := manifests.bs
		var stableBackends []*computepb.Backend
		if isBackendServiceInURLMap(urlMap, stableSvcFullName) {
			bs, err := d.gceClient.GetBackendService(ctx, &backendServiceParams{
				project: d.params.backendService.project,
				region:  d.params.backendService.region,
				name:    stableSvc,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to get stable backend service: %w", err)
			}
			stableBackends = bs.GetBackends()
		}

		if len(canaryBackends) != 0 {
			stableBackendService.Backends = canaryBackends
		} else {
			stableBackendService.Backends = stableBackends
		}
		// Pick up the changes in the new backend service template.
		if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, stableBackendService); err != nil {
			return nil, fmt.Errorf("failed to update stable backend service: %w", err)
		}

		// create the mig either if the backends is empty or it is standard deployment.
		if len(stableBackendService.GetBackends()) == 0 || percentage == 0 {
			mig, err := d.getOrCreateMIG(ctx, migName, manifests)
			if err != nil {
				return nil, err
			}
			// Update the backends of the stable backend service to reference the MIG created above.
			bes, err := generateBackendServiceBackends(ctx, d.gceClient, &d.params.mig, migName, mig, beTemplate)
			if err != nil {
				return nil, fmt.Errorf("failed to generate backend service backends: %w", err)
			}
			stableBackendService.Backends = bes
		}

		// if err != nil {
		//	// A not found error is acceptable, it means this is the first deployment.
		//	var apiErr *googleapi.Error
		//	if errors.As(err, &apiErr) && apiErr.Code != http.StatusNotFound {
		//		return nil, fmt.Errorf("failed to get stable backend service: %w", err)
		//	}
		// }
		// if stableBackendService != nil {
		//	for _, backend := range stableBackendService.Backends {
		//		parts := strings.Split(*backend.Group, "/")
		//		oldMIGs = append(oldMIGs, parts[len(parts)-1])
		//	}
		// }

		// stableBackendService.Name = proto.String(stableSvc)
		if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, stableBackendService); err != nil {
			return nil, fmt.Errorf("failed to update stable backend service: %w", err)
		}

		setRouteWeightStableWrapper := func(pm *computepb.PathMatcher, stable, canary string, p int) error {
			return setRouteWeightForStable(pm, stable, canary, int32(p))
		}
		if err := d.redirectTraffic(ctx, urlMap, stableSvcFullName, canarySvcFullName, 100, setRouteWeightStableWrapper); err != nil {
			return nil, err
		}

		// Clean up process starts here.
		for _, backend := range stableBackends {
			parts := strings.Split(*backend.Group, "/")
			migToDelete := parts[len(parts)-1]
			fmt.Printf("Deleting old stable MIG %s\n", migToDelete)
			if err := d.gceClient.DeleteMIG(ctx, &d.params.mig, migToDelete); err != nil {
				fmt.Printf("failed to delete old stable MIG %s: %v\n", migToDelete, err)
			}
		}

		if percentage == 100 {
			fmt.Printf("Resetting the backends in the canary backend service %s\n", canarySvc)
			if canaryBackendService != nil {
				canaryBackendService.Backends = nil
			}
		}
	} else {
		// The canary phase consists of the following manifest apply steps:
		//  For the first phase:
		//  1. Create the canary backend service with empty backends.
		//  2. Create the MIG for the canary backend service.
		//  3. Update the backends of the canary backend service with the MIG created in (2).
		//  4. Start splitting traffic by updating the weights of the stable and canary backend services
		//     in the specified route.
		//  For the subsequent phases:
		//  - Split traffic by updating the weights of the stable and canary backend services
		//	  in the specified route.
		if !backendServiceNameProvided {
			return nil, fmt.Errorf("backend service name is required for canary deployment")
		}
		urlMap, err := d.gceClient.GetURLMap(ctx, d.params)
		if err != nil {
			return nil, fmt.Errorf("failed to get URL map: %w", err)
		}
		if !isBackendServiceInURLMap(urlMap, stableSvcFullName) {
			return &clouddeploy.DeployResult{
				ResultStatus: clouddeploy.DeploySkipped,
				SkipMessage:  fmt.Sprintf("Stable backend service %s not found in URL map %s, skipping canary deployment", stableSvc, d.params.cloudLoadBalancerURLMap),
				Metadata: map[string]string{
					clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
					clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
				},
			}, nil
		}
		manifests.bs.Backends = nil
		manifests.bs.Name = proto.String(canarySvc)
		if isBackendServiceInURLMap(urlMap, canarySvcFullName) {
			bs, err := d.gceClient.GetBackendService(ctx, &backendServiceParams{
				project: d.params.backendService.project,
				region:  d.params.backendService.region,
				name:    canarySvc,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to get canary backend service: %w", err)
			}
			// Keep the existing backends of the canary backend service.
			manifests.bs.Backends = bs.GetBackends()
		}
		// Apply the Backend Service manifest. This also allow us to catch the BS failure before
		// creating the MIG.
		if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, manifests.bs); err != nil {
			return nil, fmt.Errorf("failed to update canary backend service: %w", err)
		}
		mig, err := d.gceClient.GetMIG(ctx, &d.params.mig, migName)
		if err != nil {
			var apiErr *googleapi.Error
			if errors.As(err, &apiErr) && apiErr.Code != http.StatusNotFound {
				return nil, fmt.Errorf("failed to get MIG: %w", err)
			}
		}
		if mig == nil {
			// Since the MIG is deterministic per release, only creates the MIG if it doesn't exist.
			if err := d.gceClient.CreateMIG(ctx, &d.params.mig, manifests.igm); err != nil {
				return nil, fmt.Errorf("failed to create MIG: %w", err)
			}
		}

		// Update the backends of the canary backend service to reference the MIG created above.
		bes, err := generateBackendServiceBackends(ctx, d.gceClient, &d.params.mig, migName, mig, beTemplate)
		if err != nil {
			return nil, fmt.Errorf("failed to generate backend service backends: %w", err)
		}
		// manifests.bs.Backends should either be nil(for the first phase) or contains the desired MIG.
		// Add more checks here for the complex scenario.
		if len(manifests.bs.Backends) != len(bes) && len(bes) != 0 {
			manifests.bs.Backends = bes
			if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, manifests.bs); err != nil {
				return nil, fmt.Errorf("failed to update canary backend service: %w", err)
			}
		}

		if err := d.redirectTraffic(ctx, urlMap, stableSvcFullName, canarySvcFullName, percentage, setRouteWeightForCanary); err != nil {
			return nil, err
		}

	}

	dr := &clouddeploy.DeployResult{
		ResultStatus: clouddeploy.DeploySucceeded,
		Metadata: map[string]string{
			clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
			clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
		},
	}
	return dr, nil
}

func (d *deployer) getOrCreateMIG(ctx context.Context, migName string, manifests *manifests) (*computepb.InstanceGroupManager, error) {
	mig, err := d.gceClient.GetMIG(ctx, &d.params.mig, migName)
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code != http.StatusNotFound {
			return nil, fmt.Errorf("failed to get MIG: %w", err)
		}
	}
	if mig == nil {
		// Since the MIG is deterministic per release, only creates the MIG if it doesn't exist.
		if err := d.gceClient.CreateMIG(ctx, &d.params.mig, manifests.igm); err != nil {
			return nil, fmt.Errorf("failed to create MIG: %w", err)
		}
	}
	return mig, nil
}

type manifests struct {
	igm *computepb.InstanceGroupManager
	bs  *computepb.BackendService
}

func (d *deployer) parseManifests(manifestPath string) (*manifests, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	var m manifests
	yamls := strings.Split(string(data), "\n---\n")
	for _, y := range yamls {
		var obj map[string]interface{}
		if err := sigsk8s.Unmarshal([]byte(y), &obj); err != nil {
			return nil, fmt.Errorf("failed to unmarshal manifest: %w", err)
		}

		kind, ok := obj["kind"].(string)
		if !ok {
			return nil, fmt.Errorf("manifest is missing kind field")
		}
		delete(obj, "kind")
		delete(obj, "apiVersion")

		metadata, ok := obj["metadata"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("manifest is missing metadata field")
		}
		delete(obj, "metadata")

		jsonBytes, err := json.Marshal(obj["spec"])
		if err != nil {
			return nil, fmt.Errorf("failed to marshal manifest: %w", err)
		}
		fmt.Printf("JSON Bytes for %s: %s\n", kind, string(jsonBytes))
		switch kind {
		case "InstanceGroupManager":
			var igm computepb.InstanceGroupManager
			if err := protojson.Unmarshal(jsonBytes, &igm); err != nil {
				return nil, fmt.Errorf("failed to unmarshal InstanceGroupManager: %w", err)
			}
			name, ok := metadata["name"].(string)
			if !ok {
				return nil, fmt.Errorf("InstanceGroupManager manifest is missing metadata.name field")
			}
			igm.Name = &name
			m.igm = &igm
		case "BackendService":
			var bs computepb.BackendService
			if err := protojson.Unmarshal(jsonBytes, &bs); err != nil {
				return nil, fmt.Errorf("failed to unmarshal BackendService: %w", err)
			}
			name, ok := metadata["name"].(string)
			if !ok {
				return nil, fmt.Errorf("BackendService manifest is missing metadata.name field")
			}
			bs.Name = &name
			m.bs = &bs
		}
	}
	if m.igm == nil {
		return nil, fmt.Errorf("InstanceGroupManager not found in manifests")
	}
	return &m, nil
}

// BackendServiceYamlSpec is the struct used for parsing the backend service YAML.
type BackendServiceYamlSpec struct {
	Spec *computepb.BackendService `json:"spec,omitempty"`
}

// ParseBackendService parses the given backend service yaml into a backend service proto.
func ParseBackendService(bytes []byte, name string) (*computepb.BackendService, error) {
	spec := &BackendServiceYamlSpec{}
	if err := yaml.Unmarshal(bytes, spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshall backend service yaml: %w", err)
	}

	bsBytes, err := json.Marshal(spec.Spec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal backend service spec: %w", err)
	}
	bs := &computepb.BackendService{}
	err = protojson.Unmarshal(bsBytes, bs)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshall backend service proto: %w", err)
	}
	bs.Name = proto.String(name)

	return bs, nil
}

// BackendServiceYamlSpec is the struct used for parsing the backend service YAML.
type MIGYamlSpec struct {
	Spec *computepb.InstanceGroupManager `json:"spec,omitempty"`
}

// ParseMIG parses the given mig yaml into a backend service proto.
func ParseMIG(bytes []byte, name string) (*computepb.InstanceGroupManager, error) {
	spec := &MIGYamlSpec{}
	if err := yaml.Unmarshal(bytes, spec); err != nil {
		return nil, fmt.Errorf("failed to unmarshall backend service yaml: %w", err)
	}

	bsBytes, err := json.Marshal(spec.Spec)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal backend service spec: %w", err)
	}
	mig := &computepb.InstanceGroupManager{}
	err = protojson.Unmarshal(bsBytes, mig)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshall backend service proto: %w", err)
	}
	mig.Name = proto.String(name)

	return mig, nil
}

func isBackendServiceInURLMap(urlMap *computepb.UrlMap, backendService string) bool {
	for _, pm := range urlMap.GetPathMatchers() {
		for _, rr := range pm.GetRouteRules() {
			if rr.GetRouteAction() != nil {
				for _, wbs := range rr.GetRouteAction().GetWeightedBackendServices() {
					if *wbs.BackendService == backendService {
						return true
					}
				}
			}
		}
		for _, wbs := range pm.GetDefaultRouteAction().GetWeightedBackendServices() {
			if *wbs.BackendService == backendService {
				return true
			}
		}
	}

	return false
}

func setRouteWeightForCanary(path *computepb.PathMatcher, stableBS, canaryBS string, percentage int) error {
	if path == nil {
		return nil
	}

	for _, rule := range path.GetRouteRules() {
		action := rule.GetRouteAction()
		if action == nil {
			continue
		}
		weightedBS := action.GetWeightedBackendServices()
		for _, dest := range weightedBS {
			if dest.GetBackendService() == stableBS {
				normalizeRouteWeight(weightedBS)
				action.WeightedBackendServices = addRouteCanaryBackendService(weightedBS, dest, canaryBS, percentage)
				break
			}
		}
	}
	action := path.GetDefaultRouteAction()
	if action == nil {
		return nil
	}
	weightedBS := action.GetWeightedBackendServices()
	for _, dest := range weightedBS {
		if dest.GetBackendService() == stableBS {
			normalizeRouteWeight(weightedBS)
			action.WeightedBackendServices = addRouteCanaryBackendService(weightedBS, dest, canaryBS, percentage)
			break
		}
	}

	return nil
}

func setRouteWeightForStable(path *computepb.PathMatcher, stableBS, canaryBS string, percentage int32) error {
	if path == nil {
		return fmt.Errorf("path not specified")
	}

	for _, r := range path.GetRouteRules() {
		if r.GetRouteAction() == nil {
			continue
		}
		err := setStableWeight(path.GetName(), stableBS, canaryBS, r.GetRouteAction())
		if err != nil {
			return err
		}
	}
	action := path.GetDefaultRouteAction()
	if action == nil {
		return nil
	}
	err := setStableWeight(path.GetName(), stableBS, canaryBS, action)
	if err != nil {
		return err
	}
	return nil
}

func setStableWeight(pathName string, stableBS string, canaryBS string, hra *computepb.HttpRouteAction) error {
	var stable *computepb.WeightedBackendService
	var canary *computepb.WeightedBackendService
	var canaryIndex int
	// Look for the stable and canary destinations.
	for i, dest := range hra.GetWeightedBackendServices() {
		if dest.GetBackendService() == stableBS {
			stable = dest
		}
		if dest.GetBackendService() == canaryBS {
			canary = dest
			canaryIndex = i
		}
	}
	if canary != nil {
		if stable == nil {
			return fmt.Errorf("backend service %q not found in path matcher %q, but its canary backend service found", stableBS, pathName)
		}
		// Reset the stable weight to its original value.
		stable.Weight = proto.Uint32(stable.GetWeight() + canary.GetWeight())
		// Remove the reference to the canary bs.
		hra.WeightedBackendServices = slices.Delete(hra.WeightedBackendServices, canaryIndex, canaryIndex+1)
	}
	return nil
}

// Normalize the weight of all WeightedBackendService of a rule.
// Wsn = ceil(Ws/(total weights of all WeightedBackendService)*100).
// Ws is the weight of the stable service. Wsn is the normalized weight of the stable service.
// Empty or zero weight will be replaced with number 100.
// Similar normalization will be applied to the weight of each WeightedBackendService.
// Note that user cannot specify the weight to be 0 in a multi-WeightedBackendService rule.
// An error "Not all weights are set to be larger than 0, please set non-zero weights or leave all weights unset" will be returned.
func normalizeRouteWeight(weightedBs []*computepb.WeightedBackendService) {
	var total uint32

	for _, dest := range weightedBs {
		if dest.GetWeight() == 0 {
			dest.Weight = proto.Uint32(1)
		}
		total += dest.GetWeight()
	}
	var normalizedTotal uint32
	for i := 0; i < len(weightedBs)-1; i++ {
		w := uint32(math.Ceil(float64(weightedBs[i].GetWeight()) / float64(total) * 100))
		weightedBs[i].Weight = proto.Uint32(w)
		normalizedTotal += w
	}
	weightedBs[len(weightedBs)-1].Weight = proto.Uint32(100 - normalizedTotal)
}

// Adds the canary backend service to the WeightedBackendService in
// the HttpRouteAction and calculates
// the weight of the canary backend service using the formula:
// Wc = ceil(Wsn*phase percentage/100); Wsc = Wsn-Wc.
// Wc is the weight of the canary service. Its value is the percentage in the deployerInput.
func addRouteCanaryBackendService(weightedBs []*computepb.WeightedBackendService, stable *computepb.WeightedBackendService, canaryBS string, percentage int) []*computepb.WeightedBackendService {
	var canary *computepb.WeightedBackendService
	for _, d := range weightedBs {
		if d.GetBackendService() == canaryBS {
			canary = d
			break
		}
	}
	// This is necessary for multi-canary phases. The weight of the stable BS needs
	// to be reset before proceeding to the next phase.
	total := stable.GetWeight()
	if canary != nil {
		total += canary.GetWeight()
	}
	wc := uint32(math.Ceil(float64(total*uint32(percentage)) / 100))
	stable.Weight = proto.Uint32(total - wc)
	if canary == nil {
		return append(weightedBs, &computepb.WeightedBackendService{BackendService: &canaryBS, Weight: &wc})

	}
	canary.Weight = proto.Uint32(wc)
	return weightedBs
}

// redirectTraffic redirects traffic between the canary backend service and
// the stable backend service by updating their weights in the specified route.
func (d *deployer) redirectTraffic(ctx context.Context, urlMap *computepb.UrlMap, stableBSFullname, canaryBSFullname string, percentage int, setRouteWeightFn func(*computepb.PathMatcher, string, string, int) error) error {
	if urlMap == nil {
		return nil
	}
	fmt.Println("Updating the weights in the url map")

	initializeBackendServiceInTheRoute(urlMap, stableBSFullname)
	for _, pm := range urlMap.PathMatchers {
		if err := setRouteWeightFn(pm, stableBSFullname, canaryBSFullname, percentage); err != nil {
			return err
		}
	}
	if err := d.gceClient.PatchURLMap(ctx, d.params, urlMap); err != nil {
		return fmt.Errorf("failed to patch URL map: %w", err)
	}

	return nil
}

// Creates the stable backend service for Cloud Deploy. It will either create
// the first rule with the stable backend service, or add the backend service to
// the existing single rule in the case of multi-target deployments.
func initializeBackendServiceInTheRoute(urlMap *computepb.UrlMap, stableBSFullname string) {
	if len(urlMap.GetPathMatchers()) > 1 {
		return
	}
	if len(urlMap.GetPathMatchers()) == 0 {
		urlMap.PathMatchers = []*computepb.PathMatcher{
			{
				Name: proto.String("path-matcher-0"),
				// RouteRules: []*computepb.HttpRouteRule{
				// 	{
				// 		Priority: proto.Int32(1),
				// 		RouteAction: &computepb.HttpRouteAction{
				// 			WeightedBackendServices: []*computepb.WeightedBackendService{
				// 				{
				// 					BackendService: &stableBSFullname,
				// 					Weight:         proto.Uint32(100),
				// 				},
				// 			},
				// 		},
				// 	},
				// },
				DefaultRouteAction: &computepb.HttpRouteAction{
					WeightedBackendServices: []*computepb.WeightedBackendService{
						{
							BackendService: &stableBSFullname,
							Weight:         proto.Uint32(100),
						},
					},
				},
			},
		}
		urlMap.DefaultService = &stableBSFullname
		return
	}
	for _, dest := range urlMap.GetPathMatchers()[0].GetDefaultRouteAction().GetWeightedBackendServices() {
		if dest.GetBackendService() == stableBSFullname {
			return
		}
	}
	for _, rule := range urlMap.GetPathMatchers()[0].GetRouteRules() {
		for _, dest := range rule.GetRouteAction().GetWeightedBackendServices() {
			if dest.GetBackendService() == stableBSFullname {
				return
			}
		}
	}
	if len(urlMap.GetPathMatchers()[0].GetRouteRules()) > 1 {
		return
	}
	//  default route action first has higher priority.
	if urlMap.GetPathMatchers()[0].GetDefaultRouteAction() != nil {
		action := urlMap.GetPathMatchers()[0].GetDefaultRouteAction()
		action.WeightedBackendServices = append(action.WeightedBackendServices, &computepb.WeightedBackendService{BackendService: &stableBSFullname, Weight: proto.Uint32(0)})

	}
	if len(urlMap.GetPathMatchers()[0].GetRouteRules()) == 1 {
		action := urlMap.GetPathMatchers()[0].GetRouteRules()[0].GetRouteAction()
		action.WeightedBackendServices = append(action.WeightedBackendServices, &computepb.WeightedBackendService{BackendService: &stableBSFullname, Weight: proto.Uint32(0)})
	}

}

// Returns either the deploy results or an error if the deploy failed.
// constructBackendServiceFullName constructs the full name of a global or regional backend service.
func constructBackendServiceFullName(project, region, name string) string {
	if strings.ToLower(region) == GLOBAL {
		return fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/global/backendServices/%s", project, name)
	}
	return fmt.Sprintf("https://www.googleapis.com/compute/v1/projects/%s/regions/%s/backendServices/%s", project, region, name)
}

// gceClientInterface provides an interface for mocking the GCE client.
type gceClientInterface interface {
	GetMIG(ctx context.Context, params *migParams, migName string) (*computepb.InstanceGroupManager, error)
	GetURLMap(ctx context.Context, params *params) (*computepb.UrlMap, error)
	GetBackendService(ctx context.Context, params *backendServiceParams) (*computepb.BackendService, error)
	UpdateBackendService(ctx context.Context, params *backendServiceParams, bs *computepb.BackendService) error
	CreateMIG(ctx context.Context, params *migParams, igm *computepb.InstanceGroupManager) error
	DeleteMIG(ctx context.Context, params *migParams, migName string) error
	PatchURLMap(ctx context.Context, params *params, urlMap *computepb.UrlMap) error
}

// Creates the computepb.backend resource with the backend template defined in manifest and the MIG created by the deployer.
func generateBackendServiceBackends(ctx context.Context, compute gceClientInterface, params *migParams, migName string, mig *computepb.InstanceGroupManager, beTemplate *computepb.Backend) ([]*computepb.Backend, error) {
	var err error
	if mig == nil {
		mig, err = compute.GetMIG(ctx, params, migName)
		if err != nil {
			return nil, err
		}
	}
	// Create the `backend` field based on the template and the previously created MIG.
	be := cpy.New(cpy.IgnoreAllUnexported()).Copy(beTemplate).(*computepb.Backend)
	be.Group = proto.String(*mig.InstanceGroup)
	bes := []*computepb.Backend{
		be,
	}

	return bes, nil
}

// Update the backends resource with the backend template defined in manifest and the existing MIGs.
func updateBackendServiceBackends(ctx context.Context, compute *GceClient, backends []*computepb.Backend, beTemplate *computepb.Backend) ([]*computepb.Backend, error) {
	var bes []*computepb.Backend
	for _, backend := range backends {
		// Pick up the new backend template and the generated NEGs.
		be := cpy.New(cpy.IgnoreAllUnexported()).Copy(beTemplate).(*computepb.Backend)
		be.Group = backend.Group
	}

	return bes, nil
}

func getOrCreateMG() {

}
