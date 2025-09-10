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
	mig                         migParams
	backendService              backendServiceParams
	instanceTemplate            string
	cloudLoadBalancerURLMap     string
}

type migParams struct {
	project                 string
	region                  string
	zone                    string
	instanceGroupManager    string
	instanceGroupManagerPath string
}

type backendServiceParams struct {
	project      string
	region       string
	name         string
	templatePath string
}

// determineParams returns the params provided in the execution environment via environment variables.
func determineParams() (*params, error) {
	migProject := os.Getenv(projectMIGEnvKey)
	bsProject := os.Getenv(projectBackendServiceEnvKey)
	if migProject == "" && bsProject == "" {
		return nil, fmt.Errorf("parameter %q or %q is required", projectMIGEnvKey, projectBackendServiceEnvKey)
	}

	migRegion := os.Getenv(regionMIGEnvKey)
	bsRegion := os.Getenv(regionBackendServiceEnvKey)
	migZone := os.Getenv(zoneMIGEnvKey)
	if migRegion == "" && migZone == "" {
		return nil, fmt.Errorf("one of parameter %q or %q is required", regionMIGEnvKey, zoneMIGEnvKey)
	}

	return &params{
		mig: migParams{
			project:                  migProject,
			region:                   migRegion,
			zone:                     migZone,
			instanceGroupManager:     os.Getenv(instanceGroupManagerEnvKey),
			instanceGroupManagerPath: os.Getenv(instanceGroupManagerPathEnvKey),
		},
		backendService: backendServiceParams{
			project:      bsProject,
			region:       bsRegion,
			name:         os.Getenv(backendServiceEnvKey),
			templatePath: os.Getenv(backendServiceTemplatePathKey),
		},
		instanceTemplate:        os.Getenv(instanceTemplateEnvKey),
		cloudLoadBalancerURLMap: os.Getenv(cloudLoadBalancerURLMapEnvKey),
	}, nil
}