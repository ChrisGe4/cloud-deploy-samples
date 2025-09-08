#!/bin/bash
# Copyright 2024 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -e

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <project_id>"
  exit 1
fi

PROJECT_ID="$1"
REGION="us-central1"
CUSTOM_TARGET_TYPE="gce"
CUSTOM_TARGET_IMAGE_NAME="gce-deployer"
GCR_HOSTNAME="gcr.io"
RENDER_IMAGE_URI="${GCR_HOSTNAME}/${PROJECT_ID}/${CUSTOM_TARGET_IMAGE_NAME}:render"
DEPLOY_IMAGE_URI="${GCR_HOSTNAME}/${PROJECT_ID}/${CUSTOM_TARGET_IMAGE_NAME}:deploy"

echo "Enabling APIs..."
gcloud services enable cloudbuild.googleapis.com clouddeploy.googleapis.com --project="${PROJECT_ID}"

echo "Building GCE deployer image..."
gcloud builds submit gce-deployer --project="${PROJECT_ID}" --tag="${RENDER_IMAGE_URI}"
gcloud builds submit gce-deployer --project="${PROJECT_ID}" --tag="${DEPLOY_IMAGE_URI}"


echo "Registering GCE custom target type..."
cat <<EOF | gcloud deploy custom-target-types apply --region="${REGION}" --project="${PROJECT_ID}" --file=-
apiVersion: deploy.cloud.google.com/v1
kind: CustomTargetType
metadata:
  name: ${CUSTOM_TARGET_TYPE}
description: GCE custom target
customActions:
  renderAction:
    gcsObject:
      bucket: cloud-deploy-custom-targets
      object: gce-deployer.tgz
  deployAction:
    containerImage: ${DEPLOY_IMAGE_URI}
EOF

echo "GCE custom target type registration complete."