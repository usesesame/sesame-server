FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go test ./... && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-migrate ./cmd/migrate && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sesame-adminctl ./cmd/adminctl

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35
COPY --from=build /out/sesame-api /sesame-api
COPY --from=build /out/sesame-migrate /sesame-migrate
# Bootstrapping the first administrator must not require a Go toolchain on the
# host, so the command ships in the image the deployment already builds.
COPY --from=build /out/sesame-adminctl /sesame-adminctl
USER nonroot:nonroot
EXPOSE 8787
ENV SESAME_API_ADDR=0.0.0.0:8787
ENTRYPOINT ["/sesame-api"]
