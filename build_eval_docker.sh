#!/usr/bin/env bash
set -e

IMAGE_NAME="${1:?usage: build_eval_docker.sh <image-name> [platform]}"
DOCKER_PLATFORM="${2:-linux/amd64}"

docker build --network host --platform "$DOCKER_PLATFORM" -f eval.Dockerfile -t "$IMAGE_NAME" .
docker run --rm --platform "$DOCKER_PLATFORM" "$IMAGE_NAME" go build ./...
