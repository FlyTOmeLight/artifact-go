# syntax=docker/dockerfile:1
# Same toolchain as go.mod; digest verified 2026-10-20.
FROM golang:1.27.1-bookworm@sha256:8d48e12ec56735e9358640898b9d9b9fcca110612ed8a5567438c0a1baa24e66 AS build
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
