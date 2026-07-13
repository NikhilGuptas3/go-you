# Multi-stage build. Contrast with the Python image (~2GB with Chrome/Firefox/
# chromedriver) — this produces a static ~15MB binary on a distroless base.

FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# CGO off => fully static binaries, runnable on distroless/scratch.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /go-you ./cmd/server
# loadgen + loadtest ship in the same image; the load Job overrides the
# entrypoint to run them in-cluster (no port-forward bottleneck).
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /loadgen ./cmd/loadgen
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /loadtest ./cmd/loadtest

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /go-you /go-you
COPY --from=build /loadgen /loadgen
COPY --from=build /loadtest /loadtest
EXPOSE 5000
USER nonroot:nonroot
ENTRYPOINT ["/go-you"]
