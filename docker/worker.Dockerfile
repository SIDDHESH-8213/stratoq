FROM golang:1.22 AS build
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/worker ./cmd/worker

FROM gcr.io/distroless/base-debian12
COPY --from=build /out/worker /worker
ENTRYPOINT ["/worker"]
