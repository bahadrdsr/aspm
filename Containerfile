FROM node:22.20.0-alpine@sha256:dbcedd8aeab47fbc0f4dd4bffa55b7c3c729a707875968d467aaaea42d6225af AS ui
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /build/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci --ignore-scripts --registry="$NPM_REGISTRY" --replace-registry-host=always --fetch-retries=1
COPY web/ ./
RUN npm run build

FROM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS backend
ARG ASPM_VERSION=0.1.0-dev.1
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/core-api ./cmd/core-api && \
    CGO_ENABLED=0 go build -trimpath -o /out/ingestion ./cmd/ingestion && \
    CGO_ENABLED=0 go build -trimpath -o /out/retention-worker ./cmd/retention-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/report-worker ./cmd/report-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/delivery-worker ./cmd/delivery-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/collection-worker ./cmd/collection-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/assessment-worker ./cmd/assessment-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/verification-worker ./cmd/verification-worker && \
    CGO_ENABLED=0 GOFLAGS="-ldflags=-X=main.version=${ASPM_VERSION}" go build -trimpath -o /out/aspmctl ./cmd/aspmctl

FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
ARG ASPM_VERSION=0.1.0-dev.1
ARG ASPM_REVISION=unknown
LABEL org.opencontainers.image.title="ASPM" \
      org.opencontainers.image.version="${ASPM_VERSION}" \
      org.opencontainers.image.revision="${ASPM_REVISION}" \
      org.opencontainers.image.source="https://github.com/bahadrdsr/aspm" \
      org.opencontainers.image.licenses="Apache-2.0"
RUN apk add --no-cache ca-certificates && addgroup -S -g 10001 aspm && \
    adduser -S -D -u 10001 -G aspm aspm
WORKDIR /app
COPY --from=backend /out/ /app/bin/
COPY --from=ui /build/web/dist/ /app/web/
COPY LICENSE NOTICE /app/
ENV ASPM_LISTEN=0.0.0.0:8080 ASPM_ASSETS=/app/web
USER 10001:10001
EXPOSE 8080
ENTRYPOINT []
CMD ["/app/bin/core-api"]
