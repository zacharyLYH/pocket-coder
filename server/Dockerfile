# Multi-stage build: compile on golang, run on plain alpine.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# The server image also carries the static Work Sync client for every
# supported laptop platform; the bootstrap downloads and verifies it.
RUN CGO_ENABLED=0 go build -o /out/pcoder ./cmd/server \
	&& for t in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do \
		os=${t%/*}; arch=${t#*/}; \
		mkdir -p /out/sync-bin/$os-$arch; \
		CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -o /out/sync-bin/$os-$arch/pcoder-sync ./cmd/pcoder-sync || exit 1; \
	done

FROM alpine:3.21
# ssh probes the deploy key (ssh -T git@github.com) and ssh-keygen mints
# it on first boot; without openssh-client both fail at runtime.
RUN apk add --no-cache ca-certificates openssh-client
COPY --from=build /out/pcoder /usr/local/bin/pcoder
COPY --from=build /out/sync-bin /usr/local/bin/sync-bin
EXPOSE 8080
ENTRYPOINT ["pcoder"]
