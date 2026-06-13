FROM node:22-alpine AS webui
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-fund --no-audit
COPY web/ .
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/mishmesh-server ./cmd/mishmesh-server
RUN mkdir -p /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/mishmesh-server /usr/local/bin/mishmesh-server
COPY --from=build --chown=65532:65532 /data /data
COPY --from=webui /web/dist /webui
ENV MISHMESH_INGRESS_ADDR=0.0.0.0:8080 \
    MISHMESH_API_ADDR=0.0.0.0:8081 \
    MISHMESH_HTTPS_ADDR=0.0.0.0:8443 \
    MISHMESH_TCP_BIND_HOST=0.0.0.0 \
    MISHMESH_TLS_PASSTHROUGH_ADDR=0.0.0.0:8444 \
    MISHMESH_DATA_DSN=/data/mishmesh.db \
    MISHMESH_ACME_CACHE_DIR=/data/certs \
    MISHMESH_WEBUI_DIR=/webui
VOLUME ["/data"]
EXPOSE 8080 8081 8443 8444
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/mishmesh-server"]
