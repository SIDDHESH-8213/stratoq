FROM golang:1.22 AS build
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/scheduler ./cmd/scheduler

FROM gcr.io/distroless/base-debian12
COPY --from=build /out/scheduler /scheduler
ENTRYPOINT ["/scheduler"]
