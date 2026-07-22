FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/bionicpro-auth .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S app -G app

COPY --from=build /out/bionicpro-auth /usr/local/bin/bionicpro-auth

USER app
EXPOSE 8000 8001

ENTRYPOINT ["/usr/local/bin/bionicpro-auth"]
