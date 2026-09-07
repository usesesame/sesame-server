FROM golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG SESAME_VERSION=0.1.0-dev
ARG SESAME_COMMIT=unknown
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X usesesame.app/backend/internal/buildinfo.Version=${SESAME_VERSION} -X usesesame.app/backend/internal/buildinfo.Commit=${SESAME_COMMIT}" -o /out/sesame-api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-adminctl ./cmd/adminctl && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-healthcheck ./cmd/healthcheck

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG SESAME_VERSION=0.1.0-dev
ARG SESAME_COMMIT=unknown
ARG SESAME_SOURCE_URL=https://github.com/usesesame/sesame-server
LABEL org.opencontainers.image.title="Sesame API" \
      org.opencontainers.image.version="${SESAME_VERSION}" \
      org.opencontainers.image.revision="${SESAME_COMMIT}" \
      org.opencontainers.image.source="${SESAME_SOURCE_URL}"
COPY --from=build /out/sesame-api /sesame-api
COPY --from=build /out/sesame-migrate /sesame-migrate
# Bootstrapping the first administrator must not require a Go toolchain on the
# host, so the command ships in the image the deployment already builds.
COPY --from=build /out/sesame-adminctl /sesame-adminctl
COPY --from=build /out/sesame-healthcheck /sesame-healthcheck
USER nonroot:nonroot
EXPOSE 8787
ENV SESAME_API_ADDR=0.0.0.0:8787
ENTRYPOINT ["/sesame-api"]
