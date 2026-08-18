#!/usr/bin/env bash
set -e

IMAGE_NAME="${1:?usage: build_eval_docker.sh <image-name> [platform]}"
DOCKER_PLATFORM="${2:-linux/amd64}"

docker build --platform "$DOCKER_PLATFORM" -f eval.Dockerfile -t "$IMAGE_NAME" .
