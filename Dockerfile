FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/homevoice ./cmd/gateway

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H homevoice
WORKDIR /app
COPY --from=build /out/homevoice /usr/local/bin/homevoice
USER homevoice
ENV HOST=0.0.0.0
EXPOSE 3000
CMD ["homevoice"]
