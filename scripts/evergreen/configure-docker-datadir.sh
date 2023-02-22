#!/usr/bin/env bash
set -Eeou pipefail
set -x
echo "Configuring Docker data directory"

if docker info 2>/dev/null | grep "Docker Root Dir" | grep -q "/var/lib/docker"; then
    # we need to reconfigure Docker so its image storage points to a
    # directory with enough space, in this case /data
    echo "Trying with /etc/docker/daemon.json file"
    sudo mkdir -p /etc/docker
    sudo chmod o+w /etc/docker
    cat <<EOF >"${HOME}/daemon.json"
{
    "data-root": "/data/docker",
    "storage-driver": "overlay2"
}
EOF
    sudo mv "${HOME}/daemon.json" /etc/docker/daemon.json

    sudo systemctl restart docker
    if docker info 2>/dev/null | grep "Docker Root Dir" | grep -q "/data/docker"; then
        echo "Docker storage configured correctly"
    else
        # The change didn't went through, we are not failing the test, but it might
        # fail because of no free space left in device.
        echo "Docker storage was not configured properly"
    fi
fi

mkdir -p logs/
docker info >logs/docker-info.text
