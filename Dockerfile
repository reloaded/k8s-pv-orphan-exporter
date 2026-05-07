# Build stage: compile a static binary on top of the latest Go release.
ARG GO_VERSION=1.25
FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# Cache module downloads in their own layer.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

COPY . .

ARG VERSION=dev
ARG REVISION=unknown
ARG BRANCH=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/reloaded/k8s-pv-orphan-exporter/internal/version.Version=${VERSION} \
        -X github.com/reloaded/k8s-pv-orphan-exporter/internal/version.Revision=${REVISION} \
        -X github.com/reloaded/k8s-pv-orphan-exporter/internal/version.Branch=${BRANCH}" \
      -o /out/k8s-pv-orphan-exporter \
      ./cmd/k8s-pv-orphan-exporter

# Runtime stage: distroless static, non-root.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/k8s-pv-orphan-exporter /usr/local/bin/k8s-pv-orphan-exporter

USER nonroot:nonroot
EXPOSE 9877

ENTRYPOINT ["/usr/local/bin/k8s-pv-orphan-exporter"]
