#!/usr/bin/env bash
# shellcheck disable=SC2046

set -e -o pipefail

export PROJECT_MODULE="github.com/traefik/traefik"
export MODULE_VERSION="v3"
IMAGE_NAME="kubernetes-codegen:latest"
CURRENT_DIR="$(pwd)"

echo "Building codegen Docker image..."

CTX_DIR=$(mktemp -d)
docker build --build-arg KUBE_VERSION=v0.29.1 \
             --build-arg USER="${USER}" \
             --build-arg UID="$(id -u)" \
             --build-arg GID="$(id -g)" \
             -f "./script/codegen.Dockerfile" \
             -t "${IMAGE_NAME}" \
             "$CTX_DIR"
rm -r "${CTX_DIR}"

echo "Generating Traefik clientSet code and DeepCopy code ..."
docker run --rm \
           -v "${CURRENT_DIR}/devcache/code-gen:/go/pkg/mod" \
           -v "${CURRENT_DIR}:/go/src/${PROJECT_MODULE}" \
           -w "/go/src/${PROJECT_MODULE}" \
           -e "PROJECT_MODULE=${PROJECT_MODULE}" \
           -e "MODULE_VERSION=${MODULE_VERSION}" \
           "${IMAGE_NAME}" \
           bash ./script/code-gen.sh

echo "Generating the CRD definitions for the documentation ..."
docker run --rm \
           -v "${CURRENT_DIR}/devcache/code-gen:/go/pkg/mod" \
           -v "${CURRENT_DIR}:/go/src/${PROJECT_MODULE}" \
           -w "/go/src/${PROJECT_MODULE}" \
           "${IMAGE_NAME}" \
           controller-gen crd:crdVersions=v1 \
           paths={./pkg/provider/kubernetes/crd/traefikio/v1alpha1/...} \
           output:dir=./docs/content/reference/dynamic-configuration/

echo "Concatenate the CRD definitions for publication and integration tests ..."
cat "${CURRENT_DIR}"/docs/content/reference/dynamic-configuration/traefik-x.io_*.yaml > "${CURRENT_DIR}"/docs/content/reference/dynamic-configuration/kubernetes-crd-definition-v1.yml
cp -f "${CURRENT_DIR}"/docs/content/reference/dynamic-configuration/kubernetes-crd-definition-v1.yml "${CURRENT_DIR}"/integration/fixtures/k8s/01-traefik-crd.yml
cp "${CURRENT_DIR}"/docs/content/reference/dynamic-configuration/traefik-x.io_*.yaml "${CURRENT_DIR}"/charts/traefik-x/crds
