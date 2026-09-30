FROM node:24-alpine AS dashboard
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY examples ./examples
RUN CGO_ENABLED=0 go build -trimpath -o /out/gateway ./cmd/gateway && \
    CGO_ENABLED=0 go build -trimpath -o /out/echo ./examples/echo && \
    CGO_ENABLED=0 go build -trimpath -o /out/devcert ./cmd/devcert && \
    CGO_ENABLED=0 go build -trimpath -o /out/check ./cmd/check && \
    CGO_ENABLED=0 go build -trimpath -o /out/loadtest ./cmd/loadtest

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 gateway && adduser -D -u 10001 -G gateway gateway && mkdir /certs && chown gateway:gateway /certs
WORKDIR /app
COPY --from=build /out/ /app/
COPY --from=dashboard /web/dist /app/web/dist
COPY configs /app/configs
USER 10001:10001
EXPOSE 8443
ENTRYPOINT ["/app/gateway"]
