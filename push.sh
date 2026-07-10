#!/usr/bin/env bash
# Deploy go-you via Helm into a segregated, port-forward-only namespace.
# Run from the repo root. Usage: ./push.sh prod <imageTag>
set -euo pipefail

ENV=${1:-dev}
IMG=${2:-}

if [ -z "$IMG" ]; then
  echo "Please provide the imageTag (from ./docker-build.sh)"
  exit 1
fi
echo "env = $ENV"

if [[ $ENV == "prod" ]]; then
  read -p "Deploy go-you POC to the prod cluster (namespace go-you-poc) [type 'goyou']? " user_input
  if [[ "$user_input" != "goyou" ]]; then
    echo 'exiting...'
    exit 1
  fi
  CLUSTER_NAME="arn:aws:eks:ap-south-1:390403892071:cluster/sign3-prod"
else
  echo "Configuration for $ENV not set"
  exit 1
fi

deploymentName="go-you"
namespace="go-you-poc"

kubectl config use-context "$CLUSTER_NAME"

# Ensure namespace exists.
kubectl get namespace "$namespace" >/dev/null 2>&1 || kubectl create namespace "$namespace"

# The go-you-secrets Secret (MYSQL_DSN, PROXY_URL) must already be applied.
# Copy deploy/secret.example.yaml -> secret.yaml, fill in real values, then:
#   kubectl apply -n go-you-poc -f secret.yaml
if ! kubectl get secret go-you-secrets -n "$namespace" >/dev/null 2>&1; then
  echo "ERROR: secret 'go-you-secrets' not found in namespace $namespace."
  echo "Create it from deploy/secret.example.yaml and:  kubectl apply -n $namespace -f secret.yaml"
  exit 1
fi

CMD="helm upgrade --install $deploymentName deploy/chart/ -n $namespace \
  --set image.tag=$IMG --set deploymentName=$deploymentName \
  -f deploy/chart/values-prod.yaml"
echo "$CMD"
eval "$CMD"

echo ""
echo "Deployed. Reach it locally with:"
echo "  kubectl port-forward -n $namespace svc/$deploymentName 8080:80"
echo "  curl localhost:8080/healthz"
