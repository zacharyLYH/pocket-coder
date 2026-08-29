# test/fixtures

Tiny, network-free fixtures so harness flows work on a laptop: a minimal
Dockerfile (alpine + python3 + iproute2) for the Docker control-plane
integration tests, and fake CLI scripts for harness phases. The Phase 6
create-pipeline gate builds its throwaway fixture git repo at runtime and
serves it with `git daemon`; the project image itself lives in
`server/project/image/`. See `docs/plan.md`.
