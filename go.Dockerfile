# Один Dockerfile для обоих Go-сервисов: SERVICE=collector | mock-marketplace
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal

FROM build AS test
RUN CGO_ENABLED=0 go vet ./... && CGO_ENABLED=0 go test ./...

FROM build AS compile
ARG SERVICE
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

FROM alpine:3.20
RUN adduser -D -u 10001 app
USER app
COPY --from=compile /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
