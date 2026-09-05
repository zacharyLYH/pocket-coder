# Browser worker image. The worker shares the project's network namespace;
# none of these ports are published to the public host.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		chromium chromium-sandbox xvfb x11vnc novnc python3-websockify socat ca-certificates \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd -m sps \
	&& mkdir -p /etc/chromium/policies/managed \
	&& echo '{"CommandLineFlagSecurityWarningsEnabled": false}' > /etc/chromium/policies/managed/sps.json

COPY start-browser /usr/local/bin/start-browser
RUN chmod +x /usr/local/bin/start-browser

# Chromium runs as non-root so its sandbox stays enabled: no --no-sandbox,
# no "unsupported command-line flag" infobar in the preview. Everything the
# worker binds (Xvfb, x11vnc, websockify, CDP) uses unprivileged ports.
USER sps

ENTRYPOINT ["/usr/local/bin/start-browser"]
