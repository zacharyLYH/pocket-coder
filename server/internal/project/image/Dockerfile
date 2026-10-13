# The shared project image: everything the platform itself needs to
# function (clone, tmux sessions, port detection, private repos) plus the
# runtimes coding CLIs expect (node, npm, python3, pip). Users install what
# else they want inside their running project.
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ENV TERM=xterm-256color
# Installers (e.g. kiro) land binaries in ~/.local/bin; without it on PATH
# the server's non-interactive `bash -lc` probes never find them.
ENV PATH="/root/.local/bin:${PATH}"

RUN apt-get update \
	&& apt-get install -y --no-install-recommends git tmux iproute2 openssh-client ca-certificates busybox curl unzip universal-ctags nodejs npm python3 python3-pip \
	&& rm -rf /var/lib/apt/lists/* \
	&& ln -sf "$(command -v busybox)" /usr/local/bin/vi

# Runtime updater, run by the user inside any terminal of the project.
COPY pcoder-update-runtime /usr/local/bin/pcoder-update-runtime
RUN chmod +x /usr/local/bin/pcoder-update-runtime

# Keep the container alive; all real work happens via docker exec.
CMD ["sleep", "infinity"]
