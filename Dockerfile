FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY backend ./backend
RUN CGO_ENABLED=0 go build -o /qa-api ./backend/cmd/api

FROM alpine:3.21
RUN addgroup -S app && adduser -S app -G app
USER app
COPY --from=build /qa-api /qa-api
EXPOSE 8080
ENTRYPOINT ["/qa-api"]
