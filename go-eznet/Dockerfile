FROM golang:1.22-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o go-eznet .

FROM debian:bookworm-slim
RUN groupadd -r eznet && useradd -r -g eznet eznet
WORKDIR /app
COPY --from=builder /src/go-eznet .
USER eznet
EXPOSE 9100 8120
ENTRYPOINT ["./go-eznet"]
