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
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/custom-targets/util/clouddeploy"
	"github.com/mholt/archiver/v3"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	sigsk8s "sigs.k8s.io/yaml"
)

// deployer implements the requestHandler interface for deploy requests.
type deployer struct {
	req       *clouddeploy.DeployRequest
	params    *params
	gcsClient *storage.Client
	gceClient *GceClient
}

// process processes a deploy request and uploads succeeded or failed results to GCS for Cloud Deploy.
func (d *deployer) process(ctx context.Context) error {
	fmt.Println("Processing deploy request")

	res, err := d.deploy(ctx)
	if err != nil {
		fmt.Printf("Deploy failed: %v\n", err)
		dr := &clouddeploy.DeployResult{
			ResultStatus:   clouddeploy.DeployFailed,
			FailureMessage: err.Error(),
			Metadata: map[string]string{
				clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
				clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
			},
		}
		fmt.Println("Uploading failed deploy results")
		rURI, err := d.req.UploadResult(ctx, d.gcsClient, dr)
		if err != nil {
			return fmt.Errorf("error uploading failed deploy results: %v", err)
		}
		fmt.Printf("Uploaded failed deploy results to %s\n", rURI)
		return err
	}

	fmt.Println("Uploading deploy results")
	rURI, err := d.req.UploadResult(ctx, d.gcsClient, res)
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
//
// Returns either the deploy results or an error if the deploy failed.
func (d *deployer) deploy(ctx context.Context) (*clouddeploy.DeployResult, error) {
	fmt.Printf("Downloading rendered configuration archive to %s\n", srcArchivePath)
	inURI, err := d.req.DownloadInput(ctx, d.gcsClient, renderedArchiveName, srcArchivePath)
	if err != nil {
		return nil, fmt.Errorf("unable to download deploy input with object suffix %s: %v", renderedArchiveName, err)
	}
	fmt.Printf("Downloaded rendered configuration archive from %s\n", inURI)

	archiveFile, err := os.Open(srcArchivePath)
	if err != nil {
		return nil, fmt.Errorf("unable to open archive file %s: %v", srcArchivePath, err)
	}
	fmt.Printf("Unarchiving rendered configuration in %s to %s\n", srcArchivePath, srcPath)
	if err := archiver.NewTarGz().Unarchive(archiveFile.Name(), srcPath); err != nil {
		return nil, fmt.Errorf("unable to unarchive rendered configuration: %v", err)
	}

	manifests, err := d.parseManifests()
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifests: %w", err)
	}

	igm := manifests.igm
	if err := d.gceClient.CreateMIG(ctx, d.params, igm); err != nil {
		return nil, fmt.Errorf("failed to create MIG: %w", err)
	}

	percentage := d.req.Percentage
	if percentage == 0 || percentage == 100 {
		// Standard deployment or promoting canary to 100%
		if err := d.gceClient.UpdateBackendService(ctx, d.params, manifests.bs, *igm.SelfLink); err != nil {
			return nil, fmt.Errorf("failed to update backend service: %w", err)
		}
		// TODO: add logic to find and delete old MIG.
	} else {
		// Canary deployment
		urlMap, err := d.gceClient.GetURLMap(ctx, d.params)
		if err != nil {
			return nil, fmt.Errorf("failed to get URL map: %w", err)
		}
		stableSvc := d.params.backendService
		if !isBackendServiceInURLMap(urlMap, stableSvc) {
			return &clouddeploy.DeployResult{
				ResultStatus: clouddeploy.DeploySkipped,
				SkipMessage:  fmt.Sprintf("Stable backend service %s not found in URL map %s, skipping canary deployment", stableSvc, d.params.cloudLoadBalancerURLMap),
				Metadata: map[string]string{
					clouddeploy.CustomTargetSourceMetadataKey:    gceDeployerSampleName,
					clouddeploy.CustomTargetSourceSHAMetadataKey: clouddeploy.GitCommit,
				},
			}, nil
		}

		//canaryBS := &computepb.BackendService{
		//	Name:        proto.String(*manifests.bs.Name + "-canary"),
		//	Description: proto.String("Canary backend service for Cloud Deploy"),
		//}
		//if err := d.gceClient.UpdateBackendService(ctx, d.params, canaryBS, *igm.SelfLink); err != nil {
		//	return nil, fmt.Errorf("failed to update canary backend service: %w", err)
		//}

		canarySvc := stableSvc + "-canary"

		for _, pm := range urlMap.PathMatchers {
			redirectTraffic
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

type manifests struct {
	igm *computepb.InstanceGroupManager
	bs  *computepb.BackendService
}

func (d *deployer) parseManifests() (*manifests, error) {
	manifestFile := filepath.Join(srcPath, "manifest.yaml")
	data, err := os.ReadFile(manifestFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	var m manifests
	yamls := strings.Split(string(data), "\n---\n")
	for _, y := range yamls {
		var typeMeta struct {
			Kind string `yaml:"kind"`
		}
		if err := sigsk8s.Unmarshal([]byte(y), &typeMeta); err != nil {
			return nil, fmt.Errorf("failed to unmarshal kind: %w", err)
		}

		jsonBytes, err := sigsk8s.YAMLToJSON([]byte(y))
		if err != nil {
			return nil, fmt.Errorf("failed to convert YAML to JSON: %w", err)
		}

		switch typeMeta.Kind {
		case "InstanceGroupManager":
			var igm computepb.InstanceGroupManager
			if err := protojson.Unmarshal(jsonBytes, &igm); err != nil {
				return nil, fmt.Errorf("failed to unmarshal InstanceGroupManager: %w", err)
			}
			m.igm = &igm
		case "BackendService":
			var bs computepb.BackendService
			if err := protojson.Unmarshal(jsonBytes, &bs); err != nil {
				return nil, fmt.Errorf("failed to unmarshal BackendService: %w", err)
			}
			m.bs = &bs
		}
	}
	if m.igm == nil {
		return nil, fmt.Errorf("InstanceGroupManager not found in manifests")
	}
	return &m, nil
}

func isBackendServiceInURLMap(urlMap *computepb.UrlMap, backendService string) bool {
	for _, pm := range urlMap.PathMatchers {
		for _, rr := range pm.RouteRules {
			if rr.RouteAction != nil {
				for _, wbs := range rr.RouteAction.WeightedBackendServices {
					if strings.HasSuffix(*wbs.BackendService, "/"+backendService) {
						return true
					}
				}
			}
		}
	}
	return false
}

func setRouteWeightForCanary(path *computepb.PathMatcher, stableBS, canaryBS string, percentage int32) error {
	if path == nil {
		return nil
	}
	for _, rule := range path.GetRouteRules() {
		if rule.GetRouteAction() == nil {
			continue
		}
		for _, dest := range rule.GetRouteAction().GetWeightedBackendServices() {
			if dest.GetBackendService() == stableBS {
				normalizeRouteWeight(rule)
				addRouteCanaryBackendService(rule.GetRouteAction(), dest, canaryBS, percentage)
				break
			}
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
		var stable *computepb.WeightedBackendService
		var canary *computepb.WeightedBackendService
		var canaryIndex int
		// Look for the stable and canary destinations.
		for i, dest := range r.GetRouteAction().GetWeightedBackendServices() {
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
				return fmt.Errorf("backend service %q not found in path matcher %q, but its canary backend service found", stableBS, path.GetName())
			}
			// Reset the stable weight to its original value.
			stable.Weight = proto.Uint32(stable.GetWeight() + canary.GetWeight())
			// Remove the reference to the canary bs.
			r.GetRouteAction().WeightedBackendServices = slices.Delete(r.GetRouteAction().WeightedBackendServices, canaryIndex, canaryIndex+1)
		}
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
func normalizeRouteWeight(rule *computepb.HttpRouteRule) {
	var total uint32
	dests := rule.GetRouteAction().GetWeightedBackendServices()
	for _, dest := range dests {
		if dest.GetWeight() == 0 {
			dest.Weight = proto.Uint32(1)
		}
		total += dest.GetWeight()
	}
	var normalizedTotal uint32
	for i := 0; i < len(dests)-1; i++ {
		w := uint32(math.Ceil(float64(dests[i].GetWeight()) / float64(total) * 100))
		dests[i].Weight = proto.Uint32(w)
		normalizedTotal += w
	}
	dests[len(dests)-1].Weight = proto.Uint32(100 - normalizedTotal)
}

// Adds the canary backend service to the WeightedBackendService in
// the HttpRouteAction and calculates
// the weight of the canary backend service using the formula:
// Wc = ceil(Wsn*phase percentage/100); Wsc = Wsn-Wc.
// Wc is the weight of the canary service. Its value is the percentage in the deployerInput.
func addRouteCanaryBackendService(action *computepb.HttpRouteAction, stable *computepb.WeightedBackendService, canaryBS string, percentage int32) {
	var canary *computepb.WeightedBackendService
	for _, d := range action.GetWeightedBackendServices() {
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
	wc := uint32(math.Ceil(float64(int32(total)*percentage) / 100))
	stable.Weight = proto.Uint32(total - wc)
	if canary == nil {
		action.WeightedBackendServices = append(action.WeightedBackendServices, &computepb.WeightedBackendService{BackendService: &canaryBS, Weight: &wc})
		return
	}
	canary.Weight = proto.Uint32(wc)
}

// redirectTraffic redirects traffic between the canary backend service and
// the stable backend service by updating their weights in the specified route.
func (d *deployer) redirectTraffic(ctx context.Context, urlMap *computepb.UrlMap, stableBSFullname, canaryBSFullname string, percentage int32, setRouteWeightFn func(*computepb.PathMatcher, string, string, int32) error) error {
	fmt.Println("Updating the weights in the url map")

	for _, pm := range urlMap.PathMatchers {
		initializeBackendServiceInTheRoute(pm, stableBSFullname)
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
func initializeBackendServiceInTheRoute(route *csm.Route, stableBSFullname string) {
	if len(route.GetRules()) > 1 {
		return
	}
	if len(route.GetRules()) == 0 {
		// 0 weight will be ignored.
		route.AppendRuleWithOneDestination(stableBSFullname, 0)
		return
	}

	for _, dest := range route.GetRules()[0].GetAction().GetDestinations() {
		if dest.GetServiceName() == stableBSFullname {
			return
		}
	}
	route.GetRules()[0].GetAction().AppendDestination(stableBSFullname, 0)
}
