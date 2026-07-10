# Multi-stage build. Contrast with the Python image (~2GB with Chrome/Firefox/
# chromedriver) — this produces a static ~15MB binary on a distroless base.

FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# CGO off => fully static binary, runnable on distroless/scratch.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /go-you ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /go-you /go-you
EXPOSE 5000
USER nonroot:nonroot
ENTRYPOINT ["/go-you"]
