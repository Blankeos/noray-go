# Load generator image (build context: repository root).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY bench/loadgen ./bench/loadgen
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/loadgen ./bench/loadgen

FROM scratch
COPY --from=build /out/loadgen /loadgen
ENTRYPOINT ["/loadgen"]
