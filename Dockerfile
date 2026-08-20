# The builder always runs on the native build platform and cross-compiles for
# the target, so multi-platform builds don't pay for QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . .

RUN test "$(go list -m)" = "github.com/kreove/omni-infra-provider-xoa" \
    && go list -find \
       ./internal/pkg/provider/meta \
       ./internal/pkg/provider/resources

# Runs natively on the build platform. The live Xen Orchestra integration test
# is skipped automatically because XOA_LIVE_ENDPOINT is unset here.
RUN go test ./...

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/omni-infra-provider-xoa \
    ./cmd/omni-infra-provider-xoa

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Omni Infrastructure Provider for Xen Orchestra"
LABEL org.opencontainers.image.description="Community Sidero Omni infrastructure provider for XCP-ng via Xen Orchestra"
LABEL org.opencontainers.image.source="https://github.com/kreove/omni-infra-provider-xoa"
LABEL org.opencontainers.image.licenses="MPL-2.0"

COPY --from=build \
    /out/omni-infra-provider-xoa \
    /omni-infra-provider-xoa

ENTRYPOINT ["/omni-infra-provider-xoa"]
