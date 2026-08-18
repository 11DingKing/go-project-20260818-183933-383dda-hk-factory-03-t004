# Runtime image for sitesync. Multi-stage, no latest tag, exposes 48557.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/sitesync-server ./cmd/server && \
    CGO_ENABLED=0 go build -o /out/opsctl ./cmd/opsctl

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/sitesync-server /app/sitesync-server
COPY --from=build /out/opsctl /app/opsctl
EXPOSE 48557
USER nonroot:nonroot
ENTRYPOINT ["/app/sitesync-server"]
