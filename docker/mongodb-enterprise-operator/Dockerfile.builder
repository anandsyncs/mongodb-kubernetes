#
# Dockerfile for Operator.
# to be called from git root
# docker build . -f docker/mongodb-enterprise-operator/Dockerfile.builder
#

FROM golang:1.19 as builder

ARG release_version
ARG log_automation_config_diff


COPY go.sum go.mod /go/src/github.com/10gen/ops-manager-kubernetes/
WORKDIR /go/src/github.com/10gen/ops-manager-kubernetes
RUN go mod download

COPY . /go/src/github.com/10gen/ops-manager-kubernetes

RUN go version
RUN git version
RUN mkdir /build && go build -o /build/mongodb-enterprise-operator \
        -buildvcs=false \
        -ldflags="-s -w -X github.com/10gen/ops-manager-kubernetes/pkg/util.OperatorVersion=${release_version} \
        -X github.com/10gen/ops-manager-kubernetes/pkg/util.LogAutomationConfigDiff=${log_automation_config_diff}"

ADD https://github.com/stedolan/jq/releases/download/jq-1.6/jq-linux64 /usr/local/bin/jq
RUN chmod +x /usr/local/bin/jq

RUN mkdir -p /data
RUN cat release.json | jq -r '.supportedImages."mongodb-agent".opsManagerMapping' > /data/om_version_mapping.json
RUN chmod +r /data/om_version_mapping.json

RUN go install github.com/go-delve/delve/cmd/dlv@latest

FROM scratch

COPY --from=builder /go/bin/dlv /data/dlv
COPY --from=builder /build/mongodb-enterprise-operator /data/
COPY --from=builder /data/om_version_mapping.json /data/om_version_mapping.json

ADD docker/mongodb-enterprise-operator/licenses /data/licenses/
