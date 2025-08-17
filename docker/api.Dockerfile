# Stage 1: build Go binary
FROM golang:1.22 AS build
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api

# Stage 2: minimal runtime image
FROM gcr.io/distroless/base-debian12
ENV PORT=8080
EXPOSE 8080
COPY --from=build /out/api /api
ENTRYPOINT ["/api"]
