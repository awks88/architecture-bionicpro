FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/reports-api .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S app -G app
COPY --from=build /out/reports-api /usr/local/bin/reports-api
USER app
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/reports-api"]
