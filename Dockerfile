FROM golang:1.26.6-alpine@sha256:3889b425f035be855a72fb4755265311293b6d414521f0a519d819df32222d83 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-adminctl ./cmd/adminctl

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/sesame-api /sesame-api
COPY --from=build /out/sesame-migrate /sesame-migrate
# Bootstrapping the first administrator must not require a Go toolchain on the
# host, so the command ships in the image the deployment already builds.
COPY --from=build /out/sesame-adminctl /sesame-adminctl
USER nonroot:nonroot
EXPOSE 8787
ENV SESAME_API_ADDR=0.0.0.0:8787
ENTRYPOINT ["/sesame-api"]
