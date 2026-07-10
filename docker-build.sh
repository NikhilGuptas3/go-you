#!/usr/bin/env bash
# Build + push the go-you image to ECR. Run from the repo root.
# Usage: ./docker-build.sh prod
set -euo pipefail

BRANCH=$(git rev-parse --abbrev-ref HEAD)
GITTAG=$(git rev-parse --short HEAD)
TAG=${BRANCH}_${GITTAG}_$(date +"%y%m%d_%I%M")
TAG=$(echo "$TAG" | sed -e 's/\//_/g')
echo "TAG = $TAG"

ENV=${1:-dev}
echo "env = $ENV"

if [[ $ENV == "prod" ]]; then
  registry=390403892071.dkr.ecr.ap-south-1.amazonaws.com/you/prod
else
  echo "Configuration for $ENV not set"
  exit 1
fi

# Authorize docker for ECR push.
aws ecr get-login-password --region ap-south-1 | docker login --username AWS --password-stdin "$registry"

# Build the Go image. Context = repo root (Dockerfile is here). linux/amd64 for EKS.
docker build --platform=linux/amd64 -t "$registry:$TAG" .
docker push "$registry:$TAG"

echo ""
echo "Pushed $registry:$TAG"
echo "Now deploy with:  ./push.sh prod $TAG"
