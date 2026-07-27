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

case "${1:-start}" in
    start)   do_start   ;;
    stop)    do_stop    ;;
    restart) do_restart ;;
    logs)    do_logs    ;;
    status)  do_status  ;;
    build)   do_build   ;;
    *)
        echo "Usage: $0 {start|stop|restart|logs|status|build}" >&2
        exit 1
        ;;
esac
