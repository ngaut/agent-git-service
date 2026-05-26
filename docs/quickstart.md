# Quick Start

This guide runs `agent-git-service` locally from source on
`http://localhost:8080` with a disposable [TiDB Zero](https://zero.tidbcloud.com/)
database for metadata.

The local development credentials are:

```text
ADMIN_LOGIN=octocat
ADMIN_TOKEN=local-dev-token
```

## Prerequisites

- Go 1.25+
- Git
- curl and jq
- GitHub CLI, if you want to try the `gh` examples

## Prepare TiDB Zero

Run this in the same terminal where you will start the server:

```bash
ZERO_INSTANCE="$(
  curl -fsS -X POST https://zero.tidbapi.com/v1beta1/instances \
    -H "Content-Type: application/json" \
    -d '{"tag":"agent-git-service-quickstart"}'
)"
export DB_DSN="$(
  printf '%s' "$ZERO_INSTANCE" | jq -r '
    .instance.connection as $c |
    "\($c.username):\($c.password)@tcp(\($c.host):\($c.port))/test?parseTime=true&timeout=10s&tls=true"
  '
)"

printf 'TiDB Zero claim URL: %s\n' "$(
  printf '%s' "$ZERO_INSTANCE" | jq -r '.instance.claimInfo.claimUrl'
)"
```

The quickstart uses the default `test` database that TiDB Zero creates for the
temporary instance.

## Start The Server

```bash
git clone https://github.com/ngaut/agent-git-service.git
cd agent-git-service
cp .env.example .env
```

The `.env` file supplies local listener and seed-user defaults. The exported
`DB_DSN` points the server at TiDB Zero.

```bash
go run ./cmd/gh-server
```

Keep this terminal open.

## Set Environment

In another terminal:

```bash
export AGS_URL=http://localhost:8080
export AGS_LOGIN=octocat
export AGS_TOKEN=local-dev-token
```

Wait for readiness. The first start can take a few minutes while
`agent-git-service` creates tables in TiDB Zero.

```bash
for i in $(seq 1 300); do
  if curl -fsS "$AGS_URL/readyz" >/dev/null; then
    echo "ready"
    break
  fi
  if [ "$i" -eq 300 ]; then
    echo "agent-git-service did not become ready within 300 seconds" >&2
    exit 1
  fi
  sleep 1
done
```

## Try It With gh

These commands use `gh api` with full local URLs and an explicit authorization
header, so they do not require `gh auth login`.

Check the authenticated user:

```bash
gh api "$AGS_URL/api/v3/user" \
  -H "Authorization: Bearer $AGS_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  --jq '{login, type, site_admin}'
```

Create a repository:

```bash
export REPO="hello-ags-$(date +%s)"

export CLONE_URL="$(
  gh api --method POST "$AGS_URL/api/v3/user/repos" \
    -H "Authorization: Bearer $AGS_TOKEN" \
    -H "Accept: application/vnd.github+json" \
    -f name="$REPO" \
    -F private=false \
    --jq '.clone_url'
)"
```

Show the repository:

```bash
gh api "$AGS_URL/api/v3/repos/$AGS_LOGIN/$REPO" \
  -H "Authorization: Bearer $AGS_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  --jq '{full_name, private, clone_url}'
```

## Try It With curl

Use this path instead of the `gh` path above if you prefer plain HTTP examples.

Check the authenticated user:

```bash
curl -fsS \
  -H "Authorization: Bearer $AGS_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  "$AGS_URL/api/v3/user" | jq '{login, type, site_admin}'
```

Create a repository:

```bash
export REPO="hello-ags-$(date +%s)"

jq -n --arg name "$REPO" '{name: $name, private: false}' > /tmp/agent-git-service-create-repo.json

export CLONE_URL="$(
  curl -fsS -X POST \
    -H "Authorization: Bearer $AGS_TOKEN" \
    -H "Accept: application/vnd.github+json" \
    -H "Content-Type: application/json" \
    -d @/tmp/agent-git-service-create-repo.json \
    "$AGS_URL/api/v3/user/repos" \
    | tee /tmp/agent-git-service-repo.json \
    | jq -r '.clone_url'
)"
```

Show the repository:

```bash
cat /tmp/agent-git-service-repo.json | jq '{full_name, private, clone_url}'
```

## Push Over Git HTTP

After creating a repository with either `gh` or `curl`, push a commit to the
returned `CLONE_URL`:

```bash
WORKDIR="$(mktemp -d)"
cd "$WORKDIR"
git init
git checkout -b main
cat > README.md <<'EOF_README'
# hello-ags

This commit was pushed to agent-git-service over Git HTTP.
EOF_README
git add README.md
git -c user.name="Agent Git Service" -c user.email="local@example.com" commit -m "Initial commit"

export AGS_BASIC_AUTH="$(printf '%s:%s' "$AGS_LOGIN" "$AGS_TOKEN" | base64 | tr -d '\n')"
git remote add origin "$CLONE_URL"
git -c http.extraHeader="Authorization: Basic $AGS_BASIC_AUTH" push -u origin main
git -c http.extraHeader="Authorization: Basic $AGS_BASIC_AUTH" ls-remote "$CLONE_URL" | grep refs/heads/main
```

You should see a commit hash followed by `refs/heads/main`.

## Troubleshooting

### `required environment variable not set: DB_DSN`

Make sure `DB_DSN` is exported in the terminal running `go run ./cmd/gh-server`, or set it in
`.env`.

### `Access denied` or connection timeout

For TiDB Zero, create a fresh instance and export the new `DB_DSN`.

If you are using a TiDB Cloud Starter instance instead of TiDB Zero, check that:

- `DB_DSN` includes `tls=true`.
- The TiDB Cloud public endpoint firewall allows your current IP address.
- The username includes the TiDB Cloud prefix shown in the Connect dialog.
- The database name in `DB_DSN` is `gh-server`.

### `401 Unauthorized`

Use the local development token from `.env`:

```text
Authorization: Bearer local-dev-token
```

## Next Steps

The TiDB Zero response includes a claim URL if you want to keep the temporary
database for longer local evaluation. For a production deployment, create a
TiDB Cloud Starter instance and follow
[`production-deployment.md`](production-deployment.md).
