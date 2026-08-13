FROM golang:1.26.2-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . .

RUN test "$(go list -m)" = "github.com/kreove/omni-infra-provider-xoa" \
    && go list -find \
       ./internal/pkg/provider/meta \
       ./internal/pkg/provider/resources

RUN go test ./...

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
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
