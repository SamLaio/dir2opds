# Keep the builder version aligned with go.mod.
FROM docker.io/golang:1.27.4-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The scratch runtime has no libc; always build a static binary.
RUN CGO_ENABLED=0 go build -o /app/dir2opds

# No shell, OS packages, CA certificates or temporary directory.
FROM scratch
COPY --from=builder /app/dir2opds /dir2opds
ENTRYPOINT ["/dir2opds"]
