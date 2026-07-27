#!/usr/bin/env bash
set -euo pipefail

CONTAINER_NAME="new-api"
IMAGE="new-api:latest"
REGISTRY_IMAGE="${REGISTRY_IMAGE:-registry.xmz.ai/new-api}"
ENV_FILE=".env"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

cd "$SCRIPT_DIR"

check_docker() {
    if ! command -v docker &>/dev/null; then
        echo "Error: docker is not installed" >&2
        exit 1
    fi
}

check_env() {
    if [[ ! -f "$ENV_FILE" ]]; then
        echo "Error: $ENV_FILE not found in $SCRIPT_DIR" >&2
        exit 1
    fi
}

do_start() {
    check_docker
    check_env

    if docker ps -q -f "name=^${CONTAINER_NAME}$" | grep -q .; then
        echo "Container '$CONTAINER_NAME' is already running"
        return
    fi

    # Remove stopped container with the same name if it exists
    if docker ps -aq -f "name=^${CONTAINER_NAME}$" | grep -q .; then
        echo "Removing stopped container '$CONTAINER_NAME'..."
        docker rm "$CONTAINER_NAME" >/dev/null
    fi

    mkdir -p data logs

    echo "Starting $CONTAINER_NAME..."
    docker run -d \
        --name "$CONTAINER_NAME" \
        --network host \
        --env-file "$ENV_FILE" \
        --restart unless-stopped \
        -v "$SCRIPT_DIR/data:/data" \
        -v "$SCRIPT_DIR/logs:/app/logs" \
        "$IMAGE"

    echo "Container '$CONTAINER_NAME' started"
}

do_stop() {
    check_docker
    if docker ps -q -f "name=^${CONTAINER_NAME}$" | grep -q .; then
        echo "Stopping $CONTAINER_NAME..."
        docker stop "$CONTAINER_NAME" >/dev/null
    fi
    if docker ps -aq -f "name=^${CONTAINER_NAME}$" | grep -q .; then
        docker rm "$CONTAINER_NAME" >/dev/null
        echo "Container '$CONTAINER_NAME' stopped and removed"
    else
        echo "Container '$CONTAINER_NAME' is not running"
    fi
}

do_restart() {
    do_stop
    do_start
}

do_logs() {
    check_docker
    docker logs -f "$CONTAINER_NAME"
}

do_status() {
    check_docker
    if docker ps -q -f "name=^${CONTAINER_NAME}$" | grep -q .; then
        docker ps -f "name=^${CONTAINER_NAME}$"
    else
        echo "Container '$CONTAINER_NAME' is not running"
    fi
}

do_build() {
    check_docker
    local image_tag="${IMAGE_TAG:-$(date +%Y%m%d)}"
    local remote_image="${REGISTRY_IMAGE}:${image_tag}"

    echo "Building image $remote_image..."
    docker build \
        -t "$IMAGE" \
        -t "$remote_image" \
        "$SCRIPT_DIR"

    echo "Pushing image $remote_image..."
    docker push "$remote_image"

    echo "Done. Pushed $remote_image"
    echo "Run '$0 restart' to apply the local $IMAGE image."
}

do_deploy() {
    do_build
    do_restart
}

do_deploy_image() {
    check_docker

    local image_archive="${1:-}"
    local image_tag="${2:-$(date +%Y%m%d)}"
    local source_image="${3:-}"
    local remote_image="${REGISTRY_IMAGE}:${image_tag}"

    if [[ -z "$image_archive" || ! -f "$image_archive" ]]; then
        echo "Error: image archive not found: ${image_archive:-<empty>}" >&2
        exit 1
    fi
    if [[ ! "$image_tag" =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
        echo "Error: invalid image tag: $image_tag" >&2
        exit 1
    fi
    if [[ -z "$source_image" ]]; then
        echo "Error: source image is required" >&2
        exit 1
    fi

    echo "Loading image $source_image..."
    docker load --input "$image_archive"
    if ! docker image inspect "$source_image" >/dev/null 2>&1; then
        echo "Error: archive did not contain $source_image" >&2
        exit 1
    fi

    docker tag "$source_image" "$IMAGE"
    docker tag "$source_image" "$remote_image"

    echo "Pushing image $remote_image..."
    docker push "$remote_image"

    do_restart

    if [[ "$source_image" != "$IMAGE" && "$source_image" != "$remote_image" ]]; then
        docker image rm "$source_image" >/dev/null
    fi

    echo "Deployed and pushed $remote_image"
}

case "${1:-start}" in
    start)   do_start   ;;
    stop)    do_stop    ;;
    restart) do_restart ;;
    logs)    do_logs    ;;
    status)  do_status  ;;
    build)   do_build   ;;
    deploy)  do_deploy  ;;
    deploy-image)
        shift
        do_deploy_image "$@"
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|logs|status|build|deploy|deploy-image}" >&2
        exit 1
        ;;
esac
