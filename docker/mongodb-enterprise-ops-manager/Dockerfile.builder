# Build compilable stuff

FROM public.ecr.aws/docker/library/golang:1.24 as readiness_builder
COPY . /go/src/github.com/10gen/ops-manager-kubernetes
WORKDIR /go/src/github.com/10gen/ops-manager-kubernetes

RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/mmsconfiguration ./docker/mongodb-enterprise-init-ops-manager/mmsconfiguration/edit_mms_configuration.go
RUN CGO_ENABLED=0 go build -a -buildvcs=false -o /data/scripts/backup-daemon-readiness-probe ./docker/mongodb-enterprise-init-ops-manager/backupdaemon_readinessprobe/backupdaemon_readiness.go

# Move binaries and scripts
FROM scratch

COPY --from=readiness_builder /data/scripts/mmsconfiguration /data/scripts/mmsconfiguration
COPY --from=readiness_builder /data/scripts/backup-daemon-readiness-probe /data/scripts/backup-daemon-readiness-probe

# After v2.0, when non-Static Agent images will be removed, please ensure to copy those files
# into ./docker/mongodb-enterprise-ops-manager directory. Leaving it this way will make the maintenance easier.
COPY ./docker/mongodb-enterprise-init-ops-manager/scripts/docker-entry-point.sh /data/scripts
COPY ./docker/mongodb-enterprise-init-ops-manager/scripts/backup-daemon-liveness-probe.sh /data/scripts
COPY ./docker/mongodb-enterprise-init-ops-manager/LICENSE /data/licenses/mongodb-enterprise-ops-manager
