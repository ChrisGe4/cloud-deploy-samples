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
	"strings"
)

// Environment variable keys whose values determine the behavior of the GCE deployer.
// Cloud Deploy transforms a deploy parameter "customTarget/gceProject" into an
// environment variable of the form "CLOUD_DEPLOY_customTarget_gceProject".
const (
	projectEnvKey                  = "CLOUD_DEPLOY_customTarget_gceProject"
	projectBackendServiceEnvKey    = "CLOUD_DEPLOY_customTarget_gceBackendServiceProject"
	regionEnvKey                   = "CLOUD_DEPLOY_customTarget_gceRegion"
	regionBackendServiceEnvKey     = "CLOUD_DEPLOY_customTarget_gceBackendServiceRegion"
	zoneEnvKey                     = "CLOUD_DEPLOY_customTarget_gceZone"
	instanceTemplateEnvKey         = "CLOUD_DEPLOY_customTarget_gceInstanceTemplate"
	instanceGroupManagerEnvKey     = "CLOUD_DEPLOY_customTarget_gceInstanceGroupManager"
	backendServiceEnvKey           = "CLOUD_DEPLOY_customTarget_gceBackendService"
	backendServiceTemplatePathKey  = "CLOUD_DEPLOY_customTarget_gceBackendServiceTemplatePath"
	instanceGroupManagerPathEnvKey = "CLOUD_DEPLOY_customTarget_gceInstanceGroupManagerPath"
	cloudLoadBalancerURLMapEnvKey  = "CLOUD_DEPLOY_customTarget_gceCloudLoadBalancerURLMap"
	instanceTemplateEnvKeyPrefix   = "CLOUD_DEPLOY_customTarget_gceInstanceTemplate_"
	targetEnvKey                   = "CLOUD_DEPLOY_TARGET"
	featureEnvKey                  = "CLOUD_DEPLOY_FEATURES"
	canaryFeature                  = "canary"
)

// params contains the deploy parameter values passed into the execution environment.
type params struct {
	mig                     migParams
	backendService          backendServiceParams
	instanceTemplate        string
	cloudLoadBalancerURLMap string
	target                  string
	isCanary                bool
}

type migParams struct {
	project string
	region  string
	zone    string
	// instanceGroupManager     string
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
	migProject := os.Getenv(projectEnvKey)
	bsProject := os.Getenv(projectBackendServiceEnvKey)
	if len(bsProject) == 0 {
		bsProject = migProject
	}
	if migProject == "" || bsProject == "" {
		return nil, fmt.Errorf("parameter %q or %q is required", projectEnvKey, projectBackendServiceEnvKey)
	}

	migRegion := os.Getenv(regionEnvKey)
	migZone := os.Getenv(zoneEnvKey)
	target := os.Getenv(targetEnvKey)
	bsRegion := os.Getenv(regionBackendServiceEnvKey)
	if len(bsRegion) == 0 {
		bsRegion = migRegion
	}
	if len(bsRegion) == 0 {
		if len(migZone) != 0 {
			parts := strings.Split(migZone, "-")
			bsRegion = fmt.Sprintf("%s-%s", parts[0], parts[1])
		}
	}
	if (migRegion == "" && migZone == "") || bsRegion == "" {
		return nil, fmt.Errorf("region or zone parameters are missing")
	}
	instanceTemplate := os.Getenv(instanceTemplateEnvKey)
	if len(os.Getenv(instanceTemplateEnvKeyPrefix+target)) != 0 {
		instanceTemplate = os.Getenv(instanceTemplateEnvKeyPrefix + target)
	}

	fmt.Println("env vars are ", os.Environ())
	if migRegion == "" && migZone == "" {
		return nil, fmt.Errorf("one of parameter %q or %q is required", regionEnvKey, zoneEnvKey)
	}
	// TODO(AGENT): validate the required fields are set.
	return &params{
		mig: migParams{
			project: migProject,
			region:  migRegion,
			zone:    migZone,
			// instanceGroupManager:     os.Getenv(instanceGroupManagerEnvKey),
			instanceGroupManagerPath: os.Getenv(instanceGroupManagerPathEnvKey),
		},
		backendService: backendServiceParams{
			project:      bsProject,
			region:       bsRegion,
			templatePath: os.Getenv(backendServiceTemplatePathKey),
		},
		instanceTemplate:        instanceTemplate,
		cloudLoadBalancerURLMap: os.Getenv(cloudLoadBalancerURLMapEnvKey),
		isCanary:                strings.Contains(os.Getenv(featureEnvKey), canaryFeature),
	}, nil
}
