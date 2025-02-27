#!/bin/bash

# Copyright Istio Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

WD=$(dirname "$0")
WD=$(cd "$WD"; pwd)
ROOT=$(dirname "$WD")

set -eux

if [[ $(command -v gcloud) ]]; then
  gcloud auth configure-docker -q
elif [[ $(command -v docker-credential-gcr) ]]; then
  docker-credential-gcr configure-docker
else
  echo "No credential helpers found, push to docker may not function properly"
fi

# Old prow image does not set this, so needed explicitly here as this is not called through make
export GO111MODULE=on

DOCKER_HUB=${DOCKER_HUB:-gcr.io/istio-testing}
GCS_BUCKET=${GCS_BUCKET:-istio-build/dev}

# Use a pinned version in case breaking changes are needed
BUILDER_SHA=b57ea8960559e39517719ac0278fe8aac79e4d56

# Reference to the next minor version of Istio
# This will create a version like 1.4-alpha.sha
NEXT_VERSION=1.4
TAG=$(git rev-parse HEAD)
VERSION="${NEXT_VERSION}-alpha.${TAG}"

# In CI we want to store the outputs to artifacts, which will preserve the build
# If not specified, we can just create a temporary directory
WORK_DIR="$(mktemp -d)/build"
mkdir -p "${WORK_DIR}"

MANIFEST=$(cat <<EOF
version: ${VERSION}
docker: ${DOCKER_HUB}
directory: ${WORK_DIR}
dependencies:
  istio:
    localpath: ${ROOT}
  cni:
    git: https://github.com/istio/cni
    auto: deps
  operator:
    git: https://github.com/istio/operator
    auto: modules
EOF
)

# "Temporary" hacks
export PATH=${GOPATH}/bin:${PATH}

# cd to not impact go.mod
(cd /tmp; go get "istio.io/release-builder@${BUILDER_SHA}")

release-builder build --manifest <(echo "${MANIFEST}")

if [[ -z "${DRY_RUN:-}" ]]; then
  release-builder publish --release "${WORK_DIR}/out" \
    --gcsbucket "${GCS_BUCKET}" --gcsaliases "${NEXT_VERSION}-dev,latest" \
    --dockerhub "${DOCKER_HUB}" --dockertags "${VERSION},${NEXT_VERSION}-dev,latest"
fi
