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

#!/bin/bash

SOURCE_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
export _CT_DOCKERFILE_LOCATION="custom-targets/gce/gce-deployer/Dockerfile"
export _CT_SRCDIR="${SOURCE_DIR}/gce-deployer"
export _CT_IMAGE_NAME=gce
export _CT_TYPE_NAME=gce
export _CT_CUSTOM_ACTION_NAME=gce-deployer
export _CT_GCS_DIRECTORY=gce
export _CT_SKAFFOLD_CONFIG_NAME=gceConfig

"${SOURCE_DIR}/../util/build_and_register.sh" "$@"