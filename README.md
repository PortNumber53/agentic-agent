# agentic-agent

CLI coding agent with optional Docker-isolated shell execution.

## Web development container

This repo includes a developer-focused image definition at `Dockerfile.webdev` with common tooling for web projects:

- git, bash, curl, wget, openssh-client
- node 22 + npm/corepack
- typescript, ts-node, eslint, prettier, vite
- python3 + pip + venv
- build-essential, make, pkg-config
- jq, ripgrep, fd, tree, unzip/zip

Build it locally:

```bash
docker build -f Dockerfile.webdev -t agentic-webdev:latest .
```

Then point agentic to it via `~/.agentic/config.json`:

```json
{
  "docker_enabled": true,
  "docker_image": "agentic-webdev:latest"
}
```

You can also set `DOCKER_IMAGE=agentic-webdev:latest` as an environment override.

The runtime now checks for local images first, and only pulls from a registry if the image is not present locally.
