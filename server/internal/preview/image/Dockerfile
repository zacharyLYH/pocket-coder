# Browser worker image. The worker shares the project's network namespace;
# none of these ports are published to the public host.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive

# tigervnc-standalone-server ships Xtigervnc: display + VNC in one process,
# with the RandR resize that Xvfb lacks. xfonts-base provides the "fixed"
# font Xtigervnc needs at startup even though Chromium draws its own text.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends \
		chromium chromium-sandbox tigervnc-standalone-server xfonts-base novnc python3-websockify socat ca-certificates \
	&& rm -rf /var/lib/apt/lists/* \
	&& useradd -m pcoder \
	&& mkdir -p /etc/chromium/policies/managed \
	&& echo '{"CommandLineFlagSecurityWarningsEnabled": false}' > /etc/chromium/policies/managed/pcoder.json

COPY start-browser /usr/local/bin/start-browser
RUN chmod +x /usr/local/bin/start-browser

# The preview entry page. It must sit beside noVNC's core/ and vendor/ so its
# relative module imports resolve under the token prefix (websockify serves
# /usr/share/novnc as the web root).
COPY pcoder.html /usr/share/novnc/pcoder.html

# Chromium runs as non-root so its sandbox stays enabled: no --no-sandbox,
# no "unsupported command-line flag" infobar in the preview. Everything the
# worker binds (Xtigervnc, websockify, CDP) uses unprivileged ports.
USER pcoder

ENTRYPOINT ["/usr/local/bin/start-browser"]
