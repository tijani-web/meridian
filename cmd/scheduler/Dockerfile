# Build the meridian-scheduler binary
FROM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace/cmd/scheduler
COPY cmd/scheduler/go.mod cmd/scheduler/go.sum ./
RUN go mod download

COPY cmd/scheduler/ ./
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -a -o /meridian-scheduler .

# Final minimal distroless image
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /meridian-scheduler .
USER 65532:65532

ENTRYPOINT ["/meridian-scheduler"]
