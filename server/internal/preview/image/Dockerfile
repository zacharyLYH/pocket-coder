# Browser worker image. The worker shares the project's network namespace;
# none of these ports are published to the public host.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		chromium xvfb x11vnc novnc python3-websockify socat ca-certificates \
	&& rm -rf /var/lib/apt/lists/*

COPY start-browser /usr/local/bin/start-browser
RUN chmod +x /usr/local/bin/start-browser

ENTRYPOINT ["/usr/local/bin/start-browser"]
