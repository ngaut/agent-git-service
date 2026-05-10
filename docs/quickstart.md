# Quick Start

This guide runs `agent-git-service` locally from source on
`http://localhost:8080` with a free
[TiDB Cloud Starter](https://www.pingcap.com/tidb-cloud-starter/) instance for
metadata.

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
- A free TiDB Cloud Starter instance

## Prepare TiDB Cloud Starter

1. Open [TiDB Cloud Starter](https://www.pingcap.com/tidb-cloud-starter/).
2. Sign in or create an account.
3. Create or open a Starter instance.
4. Open **SQL Editor** and run:

```sql
CREATE DATABASE IF NOT EXISTS `gh-server`;
```

Open **Connect**, choose the **Public** connection type, and copy the host,
port, username, and password. TiDB Cloud Starter public connections require
`tls=true` in the DSN.

Current free-tier details are listed on
[TiDB pricing](https://www.pingcap.com/pricing/).

## Start The Server

```bash
git clone https://github.com/ngaut/agent-git-service.git
cd agent-git-service
cp .env.example .env
```

The required quick-start settings are at the top of `.env`. Edit `DB_DSN`
from the TiDB Cloud Connect dialog:

```env
DB_DSN=<prefix>.root:<password>@tcp(<tidb-cloud-host>:4000)/gh-server?parseTime=true&timeout=10s&tls=true
```

Then start the server:

```bash
go run .
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
`agent-git-service` creates tables in TiDB Cloud.

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

Make sure you created `.env` and set `DB_DSN`.

### `Access denied` or connection timeout

Check that:

- `DB_DSN` includes `tls=true`.
- The TiDB Cloud public endpoint firewall allows your current IP address.
- The username includes the TiDB Cloud prefix shown in the Connect dialog.
- The database name in `DB_DSN` is `gh-server`.

### `401 Unauthorized`

Use the local development token from `.env`:

```text
Authorization: Bearer local-dev-token
```
