FROM golang:1.27-bookworm@sha256:4fa7c968ba93f1635670522dbb01a2c78150443e1385746924566079f36570ea AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go test ./... && go vet ./...
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/check ./cmd/check && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/init ./cmd/init && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ops ./cmd/ops
RUN go test -race -c -o /out/api.test ./internal/httpapi
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS runtime
WORKDIR /srv
COPY --from=build /out/api /out/worker /out/check /out/init /usr/local/bin/
COPY docs/openapi.json /srv/openapi.json
USER 10001:10001
CMD ["/usr/local/bin/api"]
FROM runtime AS tests
COPY --from=build /out/api.test /usr/local/bin/api.test
ENTRYPOINT ["/usr/local/bin/api.test", "-test.v"]
FROM docker:29-cli@sha256:5a7af0dcb0e46c803efe9b45371dd21cb500e5b4453250394ac6f06d49b6e6f1 AS tools
COPY --from=build /out/ops /usr/local/bin/ops
WORKDIR /workspace
ENTRYPOINT ["/usr/local/bin/ops"]
