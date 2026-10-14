ARG GO_VERSION=1.26
ARG ALPINE_VERSION=3.23

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /server ./cmd/server

FROM alpine:${ALPINE_VERSION}
WORKDIR /app
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 vdi
COPY --from=build /server /server
USER 10001:10001
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/server"]
