# Build and package Keel Cloud.
#
# The runtime image contains the keel-cloud binary, git (for cloning
# repositories), and the docker CLI (deployment runs talk to the host's
# Docker daemon via the mounted socket — see docker-compose.yml).

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN go build -trimpath \
    -ldflags "-X github.com/smart-minds/keel/internal/version.Version=${VERSION}" \
    -o /out/keel-cloud ./cmd/keel-cloud

FROM alpine:3.20
RUN apk add --no-cache git docker-cli ca-certificates tzdata
COPY --from=build /out/keel-cloud /usr/local/bin/keel-cloud
ENV KEEL_ADDR=:8080 \
    KEEL_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["keel-cloud"]
