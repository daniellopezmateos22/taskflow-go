# ---- build ----
FROM golang:1.25-alpine AS build
WORKDIR /app

# (Opcional pero útil) permite al toolchain descargarse solo si algún módulo pide sub-versiones
ENV GOTOOLCHAIN=auto

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# binario estático, sin CGO
ENV CGO_ENABLED=0
RUN go build -o server .

# ---- runtime ----
FROM alpine:3.19
WORKDIR /app
COPY --from=build /app/server .
EXPOSE 8080
ENV JWT_SECRET=prod-change-me
ENV POSTGRES_DSN="host=db user=postgres password=postgres dbname=taskflow port=5432 sslmode=disable TimeZone=UTC"
CMD ["./server"]
