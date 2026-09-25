# Vault

Vault is an early, runnable object-storage prototype with a Go HTTP API and a React operator console. It streams uploads to disk in 4 MiB chunks, verifies SHA-256 checksums, keeps immutable on-disk generations for safe overwrite, and writes replicas to three configured local storage directories. The default policy is three replicas with two durable acknowledgements.

## Run locally

Requirements: Go 1.22+ and Node.js 20.19+.

```powershell
npm install
npm run build
$env:VAULT_ADMIN_TOKEN = "replace-this-token"
$env:VAULT_DATA_DIR = "./vault-data"
go run .
```

Open `http://localhost:8080`. The initial dashboard token is `dev-vault-token`; set `VAULT_ADMIN_TOKEN` before running outside a local development environment. The dashboard stores its token in browser local storage.

For UI development, run `npm run dev` in one terminal and `go run .` in another. Vite proxies `/api` to port 8080.

## API

- `GET /api/health` — liveness endpoint.
- `PUT /api/objects/{key}` — stream an object; optional `If-Match: "generation"` or `If-None-Match: *`.
- `GET`, `HEAD`, `DELETE /api/objects/{key}` — read, inspect, or delete an object. `DELETE` accepts `If-Match`.
- `GET /api/ops/overview` — authenticated cluster summary, nodes, policy, and repair progress.
- `GET`, `PUT /api/ops/policy` — authenticated replica and write-acknowledgement settings.
- `POST /api/nodes/{node-id}/drain` or `/restore` — authenticated local fault-injection and drain controls.

All object and operator endpoints require `Authorization: Bearer $VAULT_ADMIN_TOKEN`.

## Current boundary

This build is a single-process prototype. Its three storage nodes are directories on one machine, node state is process-local, and metadata is one atomically replaced JSON file. It does not provide cross-host replication, consensus, quorum-based metadata failover, TLS, operator identity management, or safe rolling upgrades. Do not use it as a production distributed store. The interfaces and dashboard are in place to evolve toward independent storage-node processes and consensus-backed metadata.

## Checks

```powershell
go test ./...
npm run build
```
