# syntax=docker/dockerfile:1
# Same toolchain as go.mod; digest verified 2026-10-15.
FROM golang:1.27.0-bookworm@sha256:ded31c68586d2e49e760acc2e65a884b23d032e9bbbed0ae0c55abd3fcaf4452 AS build
WORKDIR /src
ENV GOWORK=off CGO_ENABLED=0
COPY go.mod go.sum ./
ARG GOPROXY=https://proxy.golang.org,direct
RUN GOPROXY="${GOPROXY}" go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY sdk ./sdk
COPY service ./service
COPY integration ./integration
RUN go build -trimpath -o /out/artifact-server ./cmd/server \
 && go build -trimpath -o /out/artifact-forge-server ./cmd/forge-server \
 && go build -trimpath -o /out/artifact-maintenance ./cmd/maintenance \
 && mkdir -p /out/logs /out/tmp

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/artifact-server /usr/local/bin/artifact-server
COPY --from=build /out/artifact-forge-server /usr/local/bin/artifact-forge-server
COPY --from=build /out/artifact-maintenance /usr/local/bin/artifact-maintenance
COPY --from=build --chown=65532:65532 /out/logs /app/logs
COPY --from=build --chown=65532:65532 /out/tmp /tmp
COPY --chown=65532:65532 conf /app/conf
WORKDIR /app
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/artifact-server"]
