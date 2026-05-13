FROM node:22-alpine AS frontend-builder
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS go-builder
RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /src/frontend/dist pkg/cmd/admin-dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /simple-cache ./pkg/cmd

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=go-builder /simple-cache /usr/local/bin/simple-cache
EXPOSE 5051 8080 9090 2112
ENTRYPOINT ["simple-cache"]
