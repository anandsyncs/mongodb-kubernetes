#!/usr/bin/env bash

set -Eeou pipefail
test "${MDB_BASH_DEBUG:-0}" -eq 1 && set -x

source scripts/dev/set_env_context.sh
source scripts/funcs/checks
source scripts/funcs/printing
source scripts/funcs/kubernetes

check_docker_daemon_is_running() {
  if [[ "$(uname -s)" != "Linux" ]]; then
    echo "Skipping docker daemon check when not running in Linux"
    return 0
  fi

  if systemctl is-active --quiet docker; then
      echo "Docker is already running."
  else
      echo "Docker is not running. Starting Docker..."
      # Start the Docker daemon
      sudo systemctl start docker
      for _ in {1..15}; do
        if systemctl is-active --quiet docker; then
            echo "Docker started successfully."
            return 0
        fi
        echo "Waiting for Docker to start..."
        sleep 3
      done
  fi
}

remove_element() {
  config_option="${1}"
  tmpfile=$(mktemp)
  jq 'del(.'"${config_option}"')' ~/.docker/config.json >"${tmpfile}"
  cp "${tmpfile}" ~/.docker/config.json
  rm "${tmpfile}"
}

# This is the script which performs docker authentication to different registries that we use (so far ECR and RedHat)
# As the result of this login the ~/.docker/config.json will have all the 'auth' information necessary to work with docker registries

check_docker_daemon_is_running

if [[ -f ~/.docker/config.json ]]; then
  if [[ "${RUNNING_IN_EVG:-"false"}" != "true" ]]; then
    # Check if login is actually required by making a HEAD request to ECR using existing Docker config
    echo "Checking if Docker credentials are valid..."
    ecr_auth=$(jq -r '.auths."268558157000.dkr.ecr.us-east-1.amazonaws.com".auth // empty' ~/.docker/config.json)

    if [[ -n "${ecr_auth}" ]]; then
      http_status=$(curl --head -s -o /dev/null -w "%{http_code}" --max-time 3 "https://268558157000.dkr.ecr.us-east-1.amazonaws.com/v2/dev/mongodb-kubernetes/manifests/latest" \
        -H "Authorization: Basic ${ecr_auth}" 2>/dev/null || echo "error/timeout")

      if [[ "${http_status}" != "401" && "${http_status}" != "403" && "${http_status}" != "error/timeout" ]]; then
        echo "Docker credentials are up to date - not performing the new login!"
        exit
      fi
      echo "Docker login required (HTTP status: ${http_status})"
    else
      echo "No ECR credentials found in Docker config - login required"
    fi
  fi

  title "Performing docker login to ECR registries"

  # There could be some leftovers on Evergreen
  if grep -q "credsStore" ~/.docker/config.json; then
    remove_element "credsStore"
  fi
  if grep -q "credHelpers" ~/.docker/config.json; then
    remove_element "credHelpers"
  fi
fi


echo "$(aws --version)}"

aws ecr get-login-password --region "us-east-1" | docker login --username AWS --password-stdin 268558157000.dkr.ecr.us-east-1.amazonaws.com

# by default docker tries to store credentials in an external storage (e.g. OS keychain) - not in the config.json
# We need to store it as base64 string in config.json instead so we need to remove the "credsStore" element
if grep -q "credsStore" ~/.docker/config.json; then
  remove_element "credsStore"

  # login again to store the credentials into the config.json
  aws ecr get-login-password --region "us-east-1" | docker login --username AWS --password-stdin 268558157000.dkr.ecr.us-east-1.amazonaws.com
fi

aws ecr get-login-password --region "eu-west-1" | docker login --username AWS --password-stdin 268558157000.dkr.ecr.eu-west-1.amazonaws.com

if [[ -n "${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON:-}" ]]; then
  # log in to quay.io for the mongodb/mongodb-search-community private repo
  # TODO remove once we switch to the official repo in Public Preview
  quay_io_auth_file=$(mktemp)
  docker_configjson_tmp=$(mktemp)
  echo "${COMMUNITY_PRIVATE_PREVIEW_PULLSECRET_DOCKERCONFIGJSON}" | base64 -d > "${quay_io_auth_file}"
  jq -s '.[0] * .[1]' "${quay_io_auth_file}" ~/.docker/config.json > "${docker_configjson_tmp}"
  mv "${docker_configjson_tmp}" ~/.docker/config.json
  rm "${quay_io_auth_file}"
fi

create_image_registries_secret
