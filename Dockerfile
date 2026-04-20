FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /simple-cache ./pkg/cmd

FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /simple-cache /usr/local/bin/simple-cache

EXPOSE 5051 8080 9090 2112

ENTRYPOINT ["simple-cache"]
