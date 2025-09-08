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
	"fmt"
	"os"
)

// Environment variable keys whose values determine the behavior of the GCE deployer.
// Cloud Deploy transforms a deploy parameter "customTarget/gceProject" into an
// environment variable of the form "CLOUD_DEPLOY_customTarget_gceProject".
const (
	projectMIGEnvKey               = "CLOUD_DEPLOY_customTarget_gceMigProject"
	projectBackendServiceEnvKey    = "CLOUD_DEPLOY_customTarget_gceBackendServiceProject"
	regionMIGEnvKey                = "CLOUD_DEPLOY_customTarget_gceMigRegion"
	regionBackendServiceEnvKey     = "CLOUD_DEPLOY_customTarget_gceBackendServiceRegion"
	zoneMIGEnvKey                  = "CLOUD_DEPLOY_customTarget_gceMigZone"
	instanceTemplateEnvKey         = "CLOUD_DEPLOY_customTarget_gceInstanceTemplate"
	instanceGroupManagerEnvKey     = "CLOUD_DEPLOY_customTarget_gceInstanceGroupManager"
	backendServiceEnvKey           = "CLOUD_DEPLOY_customTarget_gceBackendService"
	backendServiceTemplatePathKey  = "CLOUD_DEPLOY_customTarget_gceBackendServiceTemplatePath"
	instanceGroupManagerPathEnvKey = "CLOUD_DEPLOY_customTarget_gceInstanceGroupManagerPath"
	cloudLoadBalancerURLMapEnvKey  = "CLOUD_DEPLOY_customTarget_gceCloudLoadBalancerURLMap"
)

// params contains the deploy parameter values passed into the execution environment.
type params struct {
	project                    string
	region                     string
	zone                       string
	instanceTemplate           string
	instanceGroupManager       string
	backendService             string
	backendServiceTemplatePath string
	instanceGroupManagerPath   string
	cloudLoadBalancerURLMap    string
}

// determineParams returns the params provided in the execution environment via environment variables.
func determineParams() (*params, error) {
	project := os.Getenv(projectEnvKey)
	if project == "" {
		return nil, fmt.Errorf("parameter %q is required", projectEnvKey)
	}
	region := os.Getenv(regionEnvKey)
	zone := os.Getenv(zoneEnvKey)
	if region == "" && zone == "" {
		return nil, fmt.Errorf("one of parameter %q or %q is required", regionEnvKey, zoneEnvKey)
	}

	return &params{
		project:                    project,
		region:                     region,
		zone:                       zone,
		instanceTemplate:           os.Getenv(instanceTemplateEnvKey),
		instanceGroupManager:       os.Getenv(instanceGroupManagerEnvKey),
		backendService:             os.Getenv(backendServiceEnvKey),
		backendServiceTemplatePath: os.Getenv(backendServiceTemplatePathKey),
		instanceGroupManagerPath:   os.Getenv(instanceGroupManagerPathEnvKey),
		cloudLoadBalancerURLMap:    os.Getenv(cloudLoadBalancerURLMapEnvKey),
	}, nil
}
