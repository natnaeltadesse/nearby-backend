# syntax=docker/dockerfile:1

# ---------------------------------------------------------------- base
FROM golang:1.26-alpine AS base
WORKDIR /app
RUN apk add --no-cache git ca-certificates tzdata
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# ---------------------------------------------------------------- dev
# Hot reload. Source is bind-mounted over /app by docker-compose, so nothing
# here is baked in beyond the toolchain itself.
FROM base AS dev
ARG AIR_VERSION=v1.67.4
RUN go install github.com/air-verse/air@${AIR_VERSION}
ENV CGO_ENABLED=0
EXPOSE 8081
CMD ["air", "-c", ".air.toml"]

# ---------------------------------------------------------------- builder
FROM base AS builder
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api  ./cmd/api && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# ---------------------------------------------------------------- prod
FROM gcr.io/distroless/static-debian12:nonroot AS prod
WORKDIR /
COPY --from=builder /out/api /api
COPY --from=builder /out/worker /worker
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/api"]
