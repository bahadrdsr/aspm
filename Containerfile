FROM node:22.20.0-alpine AS ui
ARG NPM_REGISTRY=https://registry.npmjs.org
WORKDIR /build/web
COPY web/package.json web/package-lock.json web/.npmrc ./
RUN npm ci --ignore-scripts --registry="$NPM_REGISTRY" --replace-registry-host=always --fetch-retries=1
COPY web/ ./
RUN npm run build

FROM golang:1.27.1-alpine AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/core-api ./cmd/core-api && \
    CGO_ENABLED=0 go build -trimpath -o /out/ingestion ./cmd/ingestion && \
    CGO_ENABLED=0 go build -trimpath -o /out/report-worker ./cmd/report-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/delivery-worker ./cmd/delivery-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/collection-worker ./cmd/collection-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/assessment-worker ./cmd/assessment-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/aspmctl ./cmd/aspmctl

FROM alpine:3.23
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
