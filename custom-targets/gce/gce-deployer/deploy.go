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
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/cloud-deploy-samples/custom-targets/util/clouddeploy"
	"github.com/google/go-cpy/cpy"
	"github.com/mholt/archiver/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	sigsk8s "sigs.k8s.io/yaml"
)

const (
	GLOBAL = "global"
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

	beTemplate := manifests.bs.Backends[0]
	var bsRegion string
	if manifests.bs.GetRegion() == "" {
		bsRegion = GLOBAL
	}
	stableSvc := d.params.backendService.name
	stableSvcFullName := constructBackendServiceFullName(d.params.backendService.project, bsRegion, stableSvc)
	canarySvc := stableSvc + "-canary"
	canarySvcFullName := constructBackendServiceFullName(d.params.backendService.project, bsRegion, canarySvc)

	igm := manifests.igm
	if err := d.gceClient.CreateMIG(ctx, &d.params.mig, igm); err != nil {
		return nil, fmt.Errorf("failed to create MIG: %w", err)
	}

	percentage := d.req.Percentage
	if percentage == 0 || percentage == 100 {
		// Standard deployment or promoting canary to 100%
		// The stable phase consists of the following steps:
		//  1. If it is canary deployment, fetch the MIG in the canary backend service.
		//  2. Upsert the stable backend service to pick up the changes to the backend service template. Also replace the MIG with the canary MIG fetched from step 1 if needed.
		//  3. Create the MIG if it's standard deployment. Then update the backends of the stable backend service with the new MIG.
		//  4. Start redirecting traffic to stable backend service by updating the weights of the stable and canary backend services
		//     in the specified route.
		//  5. Delete the original stable MIG.
		
		var oldMIGs []string
		stableBS, err := d.gceClient.GetBackendService(ctx, &backendServiceParams{
			project: d.params.backendService.project,
			region:  d.params.backendService.region,
			name:    stableSvc,
		})
		
		

		if err != nil {                                                                                               │
         // A not found error is acceptable, it means this is the first deployment.                                │
         var apiErr *googleapi.Error                                                                               │
         if errors.As(err, &apiErr) && apiErr.Code != http.StatusNotFound {                                        │
           return nil, fmt.Errorf("failed to get stable backend service: %w", err)                                   │
       }     
		if stableBS != nil {
			for _, backend := range stableBS.Backends {
				parts := strings.Split(*backend.Group, "/")
				oldMIGs = append(oldMIGs, parts[len(parts)-1])
			}
		}

		bes, err := generateBackendServiceBackends(ctx, d.gceClient, &d.params.mig, *igm.Name, beTemplate)
		if err != nil {
			return nil, fmt.Errorf("failed to generate backend service backends: %w", err)
		}
		manifests.bs.Backends = bes
		manifests.bs.Name = proto.String(stableSvc)
		if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, manifests.bs); err != nil {
			return nil, fmt.Errorf("failed to update stable backend service: %w", err)
		}

		urlMap, err := d.gceClient.GetURLMap(ctx, d.params)
		if err != nil {
			return nil, fmt.Errorf("failed to get URL map: %w", err)
		}

		setRouteWeightStableWrapper := func(pm *computepb.PathMatcher, stable, canary string, p int) error {
			return setRouteWeightForStable(pm, stable, canary, int32(p))
		}
		if err := d.redirectTraffic(ctx, urlMap, stableSvcFullName, canarySvcFullName, 100, setRouteWeightStableWrapper); err != nil {
			return nil, err
		}

		for _, migName := range oldMIGs {
			if migName == *igm.Name {
				continue
			}
			fmt.Printf("Deleting old stable MIG %s\n", migName)
			if err := d.gceClient.DeleteMIG(ctx, &d.params.mig, migName); err != nil {
				fmt.Printf("failed to delete old stable MIG %s: %v\n", migName, err)
			}
		}

		if percentage == 100 {
			fmt.Printf("Deleting canary backend service %s\n", canarySvc)
			if err := d.gceClient.DeleteBackendService(ctx, &d.params.backendService, canarySvc); err != nil {
				fmt.Printf("failed to delete canary backend service %s: %v\n", canarySvc, err)
			}
		}
	} else {
		// The canary phase consists of the following manifest apply steps:
		//  1. Create the canary backend service with empty backends.
		//  2. Create the MIG for the canary backend service.
		//  3. Update the backends of the canary backend service with the MIG created in (2).
		//  4. Start splitting traffic by updating the weights of the stable and canary backend services
		//     in the specified route.
		urlMap, err := d.gceClient.GetURLMap(ctx, d.params)
		if err != nil {
			return nil, fmt.Errorf("failed to get URL map: %w", err)
		}
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
		// Create Backend Service. This also allow us to catch the BS failure before
		// applying the manifest.
		manifests.bs.Backends = nil
		manifests.bs.Name = proto.String(canarySvc)
		if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, manifests.bs); err != nil {
			return nil, fmt.Errorf("failed to update canary backend service: %w", err)
		}
		// Creat the MIG.
		igm := manifests.igm
		if err := d.gceClient.CreateMIG(ctx, &d.params.mig, igm); err != nil {
			return nil, fmt.Errorf("failed to create MIG: %w", err)
		}
		// Update the backends of the canary backend service with the MIG created above.
		bes, err := generateBackendServiceBackends(ctx, d.gceClient, &d.params.mig, *igm.Name, beTemplate)
		if err != nil {
			return nil, fmt.Errorf("failed to generate backend service backends: %w", err)
		}
		manifests.bs.Backends = bes
		if err := d.gceClient.UpdateBackendService(ctx, &d.params.backendService, manifests.bs); err != nil {
			return nil, fmt.Errorf("failed to update canary backend service: %w", err)
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

func setRouteWeightForCanary(path *computepb.PathMatcher, stableBS, canaryBS string, percentage int) error {
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
func addRouteCanaryBackendService(action *computepb.HttpRouteAction, stable *computepb.WeightedBackendService, canaryBS string, percentage int) {
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
	wc := uint32(math.Ceil(float64(total*uint32(percentage)) / 100))
	stable.Weight = proto.Uint32(total - wc)
	if canary == nil {
		action.WeightedBackendServices = append(action.WeightedBackendServices, &computepb.WeightedBackendService{BackendService: &canaryBS, Weight: &wc})
		return
	}
	canary.Weight = proto.Uint32(wc)
}

// redirectTraffic redirects traffic between the canary backend service and
// the stable backend service by updating their weights in the specified route.
func (d *deployer) redirectTraffic(ctx context.Context, urlMap *computepb.UrlMap, stableBSFullname, canaryBSFullname string, percentage int, setRouteWeightFn func(*computepb.PathMatcher, string, string, int) error) error {
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
		// 0 weight will be ignored.
		// todo: add the first rule with the stableBSFullname then return.
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

	urlMap.GetPathMatchers()[0].GetRouteRules()[0].GetRouteAction().WeightedBackendServices = append(urlMap.GetPathMatchers()[0].GetRouteRules()[0].GetRouteAction().WeightedBackendServices, &computepb.WeightedBackendService{BackendService: &stableBSFullname, Weight: proto.Uint32(0)})
}

// Returns either the deploy results or an error if the deploy failed.
// constructBackendServiceFullName constructs the full name of a global or regional backend service.
func constructBackendServiceFullName(project, region, name string) string {
	if strings.ToLower(region) == GLOBAL {
		return fmt.Sprintf("projects/%s/locations/global/backendServices/%s", project, name)
	}
	return fmt.Sprintf("projects/%s/locations/%s/backendServices/%s", project, region, name)
}

// Creates the computepb.backend resource with the backend template defined manifest and the MIG created by the deployer.
func generateBackendServiceBackends(ctx context.Context, compute *GceClient, params *migParams, migName string, beTemplate *computepb.Backend) ([]*computepb.Backend, error) {
	mig, err := compute.GetMIG(ctx, params, migName)
	if err != nil {
		return nil, err
	}
	// Create the `backend` field based on the template and the generated NEGs.
	be := cpy.New(cpy.IgnoreAllUnexported()).Copy(beTemplate).(*computepb.Backend)
	be.Group = proto.String(*mig.SelfLink)
	bes := []*computepb.Backend{
		be,
	}

	return bes, nil
}
