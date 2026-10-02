# Multi-stage build: compile on golang, run on plain alpine.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pcoder ./cmd/server

FROM alpine:3.21
# ssh probes the deploy key (ssh -T git@github.com) and ssh-keygen mints
# it on first boot; without openssh-client both fail at runtime.
RUN apk add --no-cache ca-certificates openssh-client
COPY --from=build /out/pcoder /usr/local/bin/pcoder
EXPOSE 8080
ENTRYPOINT ["pcoder"]
