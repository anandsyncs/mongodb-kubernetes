#!/usr/bin/env bash
#
# A script Evergreen will use to setup openshift-preflight
set -Eeou pipefail
set -x

bindir="${workdir:?}/bin"
mkdir -p "${bindir}"

echo "Downloading preflight binary"
preflight_version="1.2.1"
curl -s --retry 3 -o preflight -LO "https://github.com/redhat-openshift-ecosystem/openshift-preflight/releases/download/${preflight_version}/preflight-linux-amd64"
chmod +x preflight
mv preflight "${bindir}"
echo "Installed preflight to ${bindir}"
