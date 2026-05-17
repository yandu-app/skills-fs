# Build stage
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache ca-certificates

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X main.gitCommit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o skills-fs ./cmd/skills-fs

# Runtime stage
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/skills-fs /skills-fs

EXPOSE 8080
ENTRYPOINT ["/skills-fs"]
CMD ["webdav", "-addr", ":8080"]
