# proxysthesis

A small reverse proxy that sits in front of [Authelia](https://www.authelia.com/)'s OIDC endpoints and injects the `groups` scope into authorization and token requests.

## Why this exists

Authelia's OIDC provider only grants claims for scopes that the client explicitly requests, and it has no built-in option to extend/force the standard scope list for a client — it only supports custom scopes, which a client must ask for. So if a client application doesn't (or can't be configured to) request the `groups` scope itself, there's no server-side setting in Authelia to add it on its behalf.

`proxysthesis` works around this by transparently patching the `scope` parameter:

- On the authorization request, it adds `groups` to the `scope` query parameter (`maybeModifyQuery`).
- On the token request (`POST /api/oidc/token` with `application/x-www-form-urlencoded` body), it adds `groups` to the `scope` form parameter (`maybeModifyTokenBody`).

If no `scope` parameter is present at all, the request is passed through unmodified.

An optional `X-Debug: true` header routes the request through a debug transport that dumps the request (method, URL, headers, body) as JSON instead of forwarding it upstream — useful for inspecting exactly what a client sends. `debug_traffic: true` in the config additionally logs full outbound requests/responses for every call.

## Configuration

Config is read from `/config/config.yaml` (falls back to defaults if missing):

```yaml
listen: ":8831"
upstream: "http://localhost:9091"
debug_traffic: false
```

- `listen` — address the proxy listens on.
- `upstream` — Authelia (or other OIDC provider) base URL to forward requests to.
- `debug_traffic` — log full outbound requests/responses when true.

## Build

Build a static binary and package it into a minimal `scratch` container image:

```bash
CGO_ENABLED=0 go build
podman build -f ./Dockerfile -t proxysthesis:latest
```

## Deploy without a registry

If you don't want to publish the image or run your own registry, export it and transfer it directly to the server:

```bash
podman save -o proxysthesis.tar proxysthesis:latest
tar -tf proxysthesis.tar          # sanity-check the archive contents
scp proxysthesis.tar server:/tmp/ # or any other transfer method
```

On the server, import the image and run it:

```bash
podman load -i /tmp/proxysthesis.tar
podman run -d --name proxysthesis \
  -v /path/to/config.yaml:/config/config.yaml:ro \
  -p 8831:8831 \
  proxysthesis:latest
```

This is the simplest option for single-server deployments. If you need to distribute the image to multiple hosts or automate rollout, consider pushing to a container registry instead.
