# Build the shipped Tailwind stylesheet from the locked npm graph; do not
# rely on a stale output.css from the repository checkout.
FROM node:22-alpine AS frontend
WORKDIR /ui
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY client ./client
RUN npm run build

# Go 1.26 build: download only modules needed by imported packages.
FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly go build -ldflags="-w -s" -o main .

# Runtime: explicit FFmpeg/FFprobe, no Node/npm or Go toolchain.
FROM alpine:3.22
RUN apk --no-cache add ca-certificates ffmpeg
ARG APP_VERSION=0.0.0
LABEL app.version=$APP_VERSION
WORKDIR /app
COPY --from=builder /app/main .
COPY --from=builder /app/client ./client/
COPY --from=frontend /ui/client/assets/output.css ./client/assets/output.css
# Classic file I/O grows .part files only through requested piece extents.
# The quota guard projects sparse file growth instead of mmap full-file allocation.
ENV TORRENT_STORAGE_DEFAULT_FILE_IO=classic
ENV BITPLAY_LISTEN_HOST=0.0.0.0
EXPOSE 3347
CMD ["/app/main"]
