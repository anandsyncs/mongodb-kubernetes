# Build compilable stuff

FROM public.ecr.aws/docker/library/golang:1.24 as readiness_builder
COPY . /go/src/github.com/10gen/ops-manager-kubernetes
WORKDIR /go/src/github.com/10gen/ops-manager-kubernetes
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /readinessprobe ./mongodb-community-operator/cmd/readiness/main.go
RUN CGO_ENABLED=0 GOFLAGS=-buildvcs=false go build -o /version-upgrade-hook ./mongodb-community-operator/cmd/versionhook/main.go

FROM scratch
ARG mongodb_tools_url_ubi

COPY --from=readiness_builder /readinessprobe /data/
COPY --from=readiness_builder /version-upgrade-hook /data/version-upgrade-hook

ADD ${mongodb_tools_url_ubi} /data/mongodb_tools_ubi.tgz

COPY ./docker/mongodb-enterprise-init-database/content/probe.sh /data/probe.sh

COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher-lib.sh /data/scripts/
COPY ./docker/mongodb-enterprise-init-database/content/agent-launcher.sh /data/scripts/

COPY ./docker/mongodb-enterprise-init-database/content/LICENSE /data/licenses/
