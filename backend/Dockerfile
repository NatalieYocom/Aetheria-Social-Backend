FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/migrate ./cmd/migrate
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/delivery ./cmd/delivery
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/retention ./cmd/retention

FROM alpine:3.22

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /out/migrate /app/migrate
COPY --from=build /out/delivery /app/delivery
COPY --from=build /out/retention /app/retention
COPY migrations /app/migrations

EXPOSE 8080

CMD ["/app/api"]
