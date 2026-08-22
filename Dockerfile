FROM node:22-alpine AS web-assets
WORKDIR /assets
COPY package*.json ./
RUN npm ci --omit=dev

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
COPY --from=web-assets /assets/node_modules/@tensorflow/tfjs/dist ./node_modules/@tensorflow/tfjs/dist
COPY --from=web-assets /assets/node_modules/@tensorflow-models/speech-commands/dist ./node_modules/@tensorflow-models/speech-commands/dist
COPY public ./public
USER homevoice
ENV HOST=0.0.0.0
EXPOSE 3000
CMD ["homevoice"]
