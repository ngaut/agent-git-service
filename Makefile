# gh-server Makefile
# ─── Variables ────────────────────────────────────────────────────────────────

BINARY      = gh-server
PORT       ?= 80
UNPRIVILEGED_PORT ?= 8080
LOG_FILE    = /tmp/ghlog.txt
STARTUP_WAIT_SECONDS ?= 60
CLI_DIR     = cli
TEST_ORG   ?= testorg
TEST_TOKEN ?= mytoken
TEST_HOST  ?= github.localhost
E2E_BASE_URL ?= http://$(TEST_HOST)
TIDB_TAG    = gh-server
DB_NAME     = gh-server
TEST_DB_DSN ?= root:@tcp(127.0.0.1:4000)/$(DB_NAME)?parseTime=true&timeout=10s

# ─── Build ────────────────────────────────────────────────────────────────────

.PHONY: build
build: ## Build the gh-server binary
	go build -o $(BINARY) .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Run goimports on all Go files
	$$(go env GOPATH)/bin/goimports -w internal/ *.go

# ─── Docker ──────────────────────────────────────────────────────────────────

.PHONY: docker-build
docker-build: ## Build Docker image locally
	docker build -t gh-server:local .

.PHONY: docker-build-all
docker-build-all: docker-build ## Build backend Docker image locally

.PHONY: docker-smoke
docker-smoke: docker-build ## Build and run basic smoke test
	@echo "Starting container..."
	@docker rm -f gh-smoke 2>/dev/null || true
	docker run -d --name gh-smoke \
		-e DB_DSN="$(DB_DSN)" \
		-e PORT=8080 \
		-e LISTEN_MODE=production \
		-p 18080:8080 \
		gh-server:local
	@echo "Waiting for startup..."
	@sleep 3
	@FAIL=0; \
	curl -sf http://localhost:18080/api/v3/ > /dev/null && echo "✓ /api/v3/ responds" || { echo "✗ /api/v3/ failed"; FAIL=1; }; \
	curl -sf http://localhost:18080/api/v3/meta > /dev/null && echo "✓ /api/v3/meta responds" || { echo "✗ /api/v3/meta failed"; FAIL=1; }; \
	curl -sf http://localhost:18080/api/v3/rate_limit > /dev/null && echo "✓ /api/v3/rate_limit responds" || { echo "✗ /api/v3/rate_limit failed"; FAIL=1; }; \
	docker rm -f gh-smoke > /dev/null; \
	exit $$FAIL

# ─── Infrastructure ──────────────────────────────────────────────────────────

.PHONY: deps
deps: ## Check all required dependencies are installed
	@echo "Checking dependencies..."
	@command -v go >/dev/null 2>&1 || { echo "✗ go not found (install from https://go.dev)"; exit 1; }
	@echo "  ✓ go $$(go version | awk '{print $$3}')"
	@command -v git >/dev/null 2>&1 || { echo "✗ git not found"; exit 1; }
	@echo "  ✓ git $$(git --version | awk '{print $$3}')"
	@command -v openssl >/dev/null 2>&1 || { echo "✗ openssl not found"; exit 1; }
	@echo "  ✓ openssl"
	@echo "✓ All dependencies present"

.PHONY: test-deps
test-deps: deps ## Check test-only dependencies, including tiup playground
	@command -v tiup >/dev/null 2>&1 || { echo "✗ tiup not found (install: curl --proto '=https' --tlsv1.2 -sSf https://tiup-mirrors.pingcap.com/install.sh | sh)"; exit 1; }
	@echo "  ✓ tiup"
	@command -v mysql >/dev/null 2>&1 || { echo "✗ mysql client not found (install: brew install mysql-client)"; exit 1; }
	@echo "  ✓ mysql client"
	@echo "✓ Test dependencies present"

.PHONY: db-check
db-check: ## Verify DB_DSN points at an external TiDB/MySQL-compatible database
	@set -eu; \
	dsn="$${DB_DSN:-}"; \
	if [ -z "$$dsn" ] && [ -f .env ]; then \
		dsn="$$(sed -n 's/^[[:space:]]*DB_DSN[[:space:]]*=[[:space:]]*//p' .env | head -n 1 | sed 's/^"//;s/"$$//')"; \
	fi; \
	if [ -z "$$dsn" ]; then \
		echo "✗ DB_DSN is not configured"; \
		echo "  Production and persistent deployments must use TiDB Cloud Starter."; \
		echo "  Test-only runs may use: make test-setup"; \
		exit 1; \
	fi; \
	case "$$dsn" in \
		*"@tcp("*|tcp\(*) \
			;; \
		*) \
			echo "✗ DB_DSN must use tcp(<tidb-cloud-host>:4000)"; \
			echo "  Production and persistent deployments must use TiDB Cloud Starter."; \
			echo "  Test-only runs may use: make test-setup"; \
			exit 1; \
			;; \
	esac; \
	case "$$dsn" in \
		*"tcp(127.0.0.1"*|*"tcp(localhost"*|*"tcp([::1]"*|*"tcp(0.0.0.0"*) \
			echo "✗ DB_DSN points at a local database"; \
			echo "  Use TiDB Cloud Starter for production/persistent setup."; \
			echo "  Use make test-setup only for test runs."; \
			exit 1; \
			;; \
	esac; \
	echo "✓ DB_DSN configured (external database)"

.PHONY: test-db-start
test-db-start: ## Start test-only TiDB via tiup playground
	@if tiup playground display --tag $(TIDB_TAG) 2>/dev/null | grep -q 'tidb'; then \
		echo "✓ TiDB already running"; \
	else \
		echo "Starting TiDB playground..."; \
		tiup clean $(TIDB_TAG) 2>/dev/null || true; \
		setsid tiup playground --tag $(TIDB_TAG) --db 1 --pd 1 --kv 1 --tiflash 0 --without-monitor > /tmp/tiup-playground.log 2>&1 < /dev/null & \
		echo "Waiting for TiDB to be ready..."; \
		for i in $$(seq 1 30); do \
			if mysql -h 127.0.0.1 -P 4000 -u root -e "SELECT 1" >/dev/null 2>&1; then \
				echo "✓ TiDB ready"; \
				break; \
			fi; \
			if [ "$$i" = "30" ]; then \
				echo "✗ TiDB failed to start (see /tmp/tiup-playground.log)"; \
				exit 1; \
			fi; \
			sleep 2; \
		done; \
	fi
	@mysql -h 127.0.0.1 -P 4000 -u root -e "CREATE DATABASE IF NOT EXISTS \`$(DB_NAME)\`" 2>/dev/null
	@echo "✓ Database '$(DB_NAME)' ready"

.PHONY: test-db-stop
test-db-stop: ## Stop test-only TiDB playground
	@data_dir="$(HOME)/.tiup/data/$(TIDB_TAG)/"; \
	tag_flag="--tag $(TIDB_TAG)"; \
	live_pids() { \
		ps -eo pid=,comm=,stat=,args= | awk -v data_dir="$$data_dir" -v tag_flag="$$tag_flag" '\
			$$3 !~ /^Z/ && (($$2 == "tidb-server" || $$2 == "tikv-server" || $$2 == "pd-server") && index($$0, data_dir) || $$2 == "tiup-playground" && index($$0, tag_flag)) { print $$1 }'; \
	}; \
	if tiup clean $(TIDB_TAG) 2>/dev/null; then \
		stopped=0; \
		for i in $$(seq 1 20); do \
			pids="$$(live_pids)"; \
			if [ -z "$$pids" ]; then \
				stopped=1; \
				break; \
			fi; \
			sleep 1; \
		done; \
		if [ "$$stopped" != "1" ]; then \
			echo "⚠ TiDB playground still running after tiup clean; forcing shutdown"; \
			pids="$$(live_pids)"; \
			if [ -n "$$pids" ]; then \
				ps -o pid=,ppid=,stat=,cmd= -p $$pids 2>/dev/null || true; \
				kill $$pids 2>/dev/null || true; \
				sleep 2; \
				pids="$$(live_pids)"; \
				if [ -n "$$pids" ]; then \
					kill -9 $$pids 2>/dev/null || true; \
				fi; \
			fi; \
			for i in $$(seq 1 5); do \
				pids="$$(live_pids)"; \
				if [ -z "$$pids" ]; then \
					stopped=1; \
					break; \
				fi; \
				sleep 1; \
			done; \
		fi; \
		if [ "$$stopped" = "1" ]; then \
			echo "✓ TiDB stopped"; \
		else \
			echo "✗ TiDB clean reported success but TiDB playground processes are still running"; \
			pids="$$(live_pids)"; \
			if [ -n "$$pids" ]; then \
				ps -o pid=,ppid=,stat=,cmd= -p $$pids 2>/dev/null || true; \
			fi; \
			exit 1; \
		fi; \
	else \
		echo "✗ Failed to stop TiDB (tiup clean failed)"; \
		exit 1; \
	fi

.PHONY: test-db-status
test-db-status: ## Show test-only TiDB playground status
	@tiup playground display --tag $(TIDB_TAG) 2>/dev/null || echo "TiDB not running"

.PHONY: certs
certs: ## Generate self-signed TLS certificates for github.localhost
	@if [ -f cert.pem ] && [ -f key.pem ]; then \
		echo "✓ TLS certs already exist (cert.pem, key.pem)"; \
	else \
		openssl req -x509 -newkey rsa:2048 -keyout key.pem -out cert.pem \
			-days 365 -nodes -subj "/CN=github.localhost" \
			-addext "subjectAltName=DNS:github.localhost,DNS:localhost,IP:127.0.0.1" 2>/dev/null; \
		echo "✓ Generated cert.pem and key.pem"; \
	fi

.PHONY: env
env: ## Create .env file if it doesn't exist
	@if [ -f .env ]; then \
		echo "✓ .env already exists"; \
	else \
		printf '# DB_DSN must point at an external TiDB/MySQL-compatible database.\n# Use TiDB Cloud Starter for production and persistent deployments.\nDB_DSN=""\nBASE_URL="http://github.localhost"\nENVIRONMENT="development"\n' > .env; \
		echo "✓ Created .env"; \
	fi
	@if grep -Eq '^[[:space:]]*ENVIRONMENT[[:space:]]*=' .env; then \
		if grep -Eq '^[[:space:]]*ENVIRONMENT[[:space:]]*=[[:space:]]*"?development"?[[:space:]]*$$' .env; then \
			echo "✓ .env sets ENVIRONMENT=development"; \
		else \
			sed -i -E 's|^[[:space:]]*ENVIRONMENT[[:space:]]*=.*$$|ENVIRONMENT="development"|' .env; \
			echo "✓ Updated .env: ENVIRONMENT=\"development\" for local acceptance tests"; \
		fi; \
	else \
		echo 'ENVIRONMENT="development"' >> .env; \
		echo "✓ Added ENVIRONMENT=\"development\" to .env for local acceptance tests"; \
	fi
	@if grep -Eq '^[[:space:]]*BASE_URL[[:space:]]*=' .env; then \
		if grep -Eq '^[[:space:]]*BASE_URL[[:space:]]*=[[:space:]]*"?http://github\.localhost(:80)?"?[[:space:]]*$$' .env; then \
			echo "✓ .env BASE_URL is compatible with acceptance tests"; \
		elif grep -Eq '^[[:space:]]*BASE_URL[[:space:]]*=[[:space:]]*"?http://github\.localhost:8080"?[[:space:]]*$$' .env; then \
			sed -i -E 's|^[[:space:]]*BASE_URL[[:space:]]*=.*$$|BASE_URL=\"http://github.localhost\"|' .env; \
			echo "✓ Updated .env: BASE_URL=\"http://github.localhost\" (avoid HTTP-on-HTTPS :8080 EOF)"; \
		else \
			echo "⚠ .env has a custom BASE_URL; acceptance tests assume http://github.localhost"; \
		fi; \
	else \
		echo 'BASE_URL="http://github.localhost"' >> .env; \
		echo "✓ Added BASE_URL=\"http://github.localhost\" to .env"; \
	fi

.PHONY: test-env
test-env: ## Write a test-only .env that targets tiup playground
	@printf '# Test-only environment. Do not use this for production deployments.\nDB_DSN="$(TEST_DB_DSN)"\nBASE_URL="http://github.localhost"\nENVIRONMENT="development"\n' > .env
	@echo "✓ Created test .env for TiDB playground"

.PHONY: hosts
hosts: ## Add github.localhost entries to /etc/hosts (requires sudo)
	@if grep -q 'github.localhost' /etc/hosts 2>/dev/null; then \
		echo "✓ /etc/hosts already has github.localhost"; \
	else \
		echo "Adding github.localhost to /etc/hosts (requires sudo)..."; \
		sudo sh -c 'echo "127.0.0.1 github.localhost api.github.localhost uploads.github.localhost" >> /etc/hosts'; \
		echo "✓ Added github.localhost entries to /etc/hosts"; \
	fi

.PHONY: setup
setup: deps env db-check certs hosts build ## Production/persistent setup using an external DB_DSN
	@echo ""
	@echo "═══════════════════════════════════════════════════"
	@echo "  Setup complete! Next steps:"
	@echo "  1. Ensure DB_DSN points at TiDB Cloud Starter"
	@echo "  2. sudo make run-bg  # start privileged listeners on :80/:443"
	@echo "     (or: make run-bg  # falls back to :8080 without sudo)"
	@echo "  3. make test      # run acceptance tests against the running server"
	@echo "═══════════════════════════════════════════════════"

.PHONY: test-setup
test-setup: test-deps certs test-env hosts test-db-start build ## Test-only setup using tiup playground
	@echo ""
	@echo "═══════════════════════════════════════════════════"
	@echo "  Test setup complete! Next steps:"
	@echo "  1. sudo make run-bg  # start privileged listeners on :80/:443"
	@echo "     (or: make run-bg  # falls back to :8080 without sudo)"
	@echo "  2. make test      # run acceptance tests"
	@echo "═══════════════════════════════════════════════════"

# ─── Run ──────────────────────────────────────────────────────────────────────

.PHONY: run
run: build ## Build and run (requires sudo for port 80)
	sudo ./$(BINARY)

.PHONY: run-bg
run-bg: build ## Build and run in background (auto-detects sudo, falls back to unprivileged mode)
	@# Try privileged mode first (for port 80/443) without any interactive sudo prompt.
	@# Launch in a fresh session so long-running acceptance suites do not inherit this shell's lifecycle.
	@health_urls=""; \
	if timeout 5 sudo -n true 2>/dev/null; then \
		echo "✓ passwordless sudo available, starting privileged listeners on :80/:443"; \
		PID=$$(pgrep -x -n "$(BINARY)" 2>/dev/null) && [ -n "$$PID" ] && ps -p $$PID > /dev/null 2>&1 && sudo -n kill $$PID 2>/dev/null || true; \
		sleep 1; \
		setsid sudo -n -E ./$(BINARY) < /dev/null > $(LOG_FILE) 2>&1 & \
		health_urls="http://$(TEST_HOST)/readyz http://127.0.0.1/readyz"; \
	else \
		echo "⚠ sudo unavailable in this context, falling back to unprivileged mode (port $(UNPRIVILEGED_PORT))"; \
		echo "⚠ Note: acceptance tests require http://$(TEST_HOST) on port 80 and will fail fast otherwise"; \
		echo "⚠ If sudo with prompt is available, run: sudo make run-bg"; \
		PID=$$(pgrep -x -n "$(BINARY)" 2>/dev/null) && [ -n "$$PID" ] && ps -p $$PID > /dev/null 2>&1 && kill $$PID 2>/dev/null || true; \
		sleep 1; \
		setsid env LISTEN_MODE=production PORT=$(UNPRIVILEGED_PORT) ./$(BINARY) < /dev/null > $(LOG_FILE) 2>&1 & \
		health_urls="http://localhost:$(UNPRIVILEGED_PORT)/readyz http://127.0.0.1:$(UNPRIVILEGED_PORT)/readyz"; \
	fi; \
	for i in $$(seq 1 $(STARTUP_WAIT_SECONDS)); do \
		sleep 1; \
		for url in $$health_urls; do \
			if curl -sf -o /dev/null "$$url" 2>/dev/null; then \
				echo "✓ Server running ($$url)"; \
				exit 0; \
			fi; \
		done; \
	done; \
	echo "✗ Server failed to start after $(STARTUP_WAIT_SECONDS)s (check $(LOG_FILE))"; \
	tail -n 20 $(LOG_FILE) 2>/dev/null || true; \
	exit 1

.PHONY: stop
stop: ## Stop running server
	-sudo pkill -x "$(BINARY)" 2>/dev/null
	@echo "✓ Server stopped"

.PHONY: restart
restart: stop run-bg ## Rebuild, stop, and restart

.PHONY: logs
logs: ## Tail server logs
	tail -f $(LOG_FILE)

.PHONY: status
status: ## Show status of all services
	@echo "=== Database ==="
	@if $${MAKE:-make} --no-print-directory db-check >/dev/null 2>&1; then \
		echo "  ✓ External DB_DSN configured"; \
	else \
		echo "  ✗ External DB_DSN not configured"; \
	fi
	@echo ""
	@echo "=== gh-server ==="
	@curl -sf -o /dev/null http://$(TEST_HOST)/api/v3/ && echo "  ✓ Responding on http://$(TEST_HOST)" || echo "  ✗ Not responding"

# ─── Tests ────────────────────────────────────────────────────────────────────

.PHONY: test-preflight
test-preflight: ## Validate acceptance host before running acceptance tests
	@if curl -sf -o /dev/null http://$(TEST_HOST)/api/v3/; then \
		echo "✓ Acceptance host reachable on http://$(TEST_HOST)"; \
	else \
		echo "✗ Acceptance host is not reachable on http://$(TEST_HOST)/api/v3/"; \
		if curl -sf -o /dev/null http://localhost:$(UNPRIVILEGED_PORT)/api/v3/; then \
			echo "  Detected gh-server on :$(UNPRIVILEGED_PORT) only."; \
			echo "  This usually means 'make run-bg' fell back without sudo."; \
			echo "  GH acceptance tests require $(TEST_HOST) on port 80 (hostname-only)."; \
			echo "  Start privileged listeners first: sudo make run-bg"; \
		else \
			echo "  gh-server does not look healthy. Start it with: make run-bg"; \
		fi; \
		exit 1; \
	fi
	@if curl -sf -o /dev/null -H "Authorization: token $(TEST_TOKEN)" http://api.$(TEST_HOST)/user; then \
		echo "✓ Acceptance token is valid on api.$(TEST_HOST)"; \
	else \
		echo "✗ Acceptance token is rejected by api.$(TEST_HOST)"; \
		echo "  This usually means seed data was skipped (ENVIRONMENT=production)."; \
		echo "  Run: make env && make stop && sudo make run-bg"; \
		exit 1; \
	fi
	@if curl -sf http://$(TEST_HOST)/api/v3/ | tr -d '\n' | grep -q '"current_user_url":"http://github.localhost:8080/api/v3/user"'; then \
		echo "✗ Server metadata exposes BASE_URL as http://github.localhost:8080"; \
		echo "  This causes release API calls to hit HTTP on HTTPS port 8080 and fail with EOF."; \
		echo "  Run: make env && make stop && sudo make run-bg"; \
		exit 1; \
	else \
		echo "✓ Server metadata BASE_URL is compatible with acceptance tests"; \
	fi

.PHONY: test
test: test-preflight ## Run acceptance tests (requires running server)
	cd $(CLI_DIR) && \
		GH_ACCEPTANCE_HOST=$(TEST_HOST) \
		GH_ACCEPTANCE_ORG=$(TEST_ORG) \
		GH_ACCEPTANCE_TOKEN=$(TEST_TOKEN) \
		go test -tags acceptance ./acceptance/ -count=1 -v -timeout 300s

.PHONY: test-unit
test-unit: ## Run all unit tests
	go test -v ./...

.PHONY: test-integration
test-integration: ## Run stable real-router integration packages
	bash scripts/integration_tests.sh

.PHONY: test-all
test-all: test-unit test ## Run all unit and acceptance tests

.PHONY: test-run
test-run: test-preflight ## Run a single test suite, e.g. make test-run SUITE=TestIssues
	cd $(CLI_DIR) && \
		GH_ACCEPTANCE_HOST=$(TEST_HOST) \
		GH_ACCEPTANCE_ORG=$(TEST_ORG) \
		GH_ACCEPTANCE_TOKEN=$(TEST_TOKEN) \
		go test -tags acceptance ./acceptance/ -count=1 -v -timeout 300s -run $(SUITE)

.PHONY: test-e2e
test-e2e: ## Run end-to-end tests. Usage: make test-e2e [SCRIPT=repo-rollback-compensation] [E2E_BASE_URL=http://...]
	@if [ -z "$(SCRIPT)" ]; then \
		for s in $$(find e2e -maxdepth 1 -type f -name "*.sh" ! -name "run.sh" ! -name "lib.sh" | sort); do \
			script="$$(basename "$$s" .sh)"; \
			E2E_BASE_URL="$(E2E_BASE_URL)" SCRIPT="$$script" bash e2e/run.sh || exit $$?; \
		done; \
	else \
		E2E_BASE_URL="$(E2E_BASE_URL)" SCRIPT="$(SCRIPT)" bash e2e/run.sh; \
	fi

.PHONY: test-script
test-script: test-preflight ## Run a single test script, e.g. make test-script SUITE=TestIssues SCRIPT=issue-create.txtar
	cd $(CLI_DIR) && \
		GH_ACCEPTANCE_HOST=$(TEST_HOST) \
		GH_ACCEPTANCE_ORG=$(TEST_ORG) \
		GH_ACCEPTANCE_TOKEN=$(TEST_TOKEN) \
		GH_ACCEPTANCE_SCRIPT=$(SCRIPT) \
		go test -tags acceptance ./acceptance/ -count=1 -v -timeout 300s -run $(SUITE)

# ─── Verify ───────────────────────────────────────────────────────────────────

.PHONY: check
check: build vet ## Build + vet (fast pre-commit check)
	@echo "✓ Build and vet passed"

.PHONY: audit
audit: ## Report remaining db.DB calls outside service layer
	@echo "=== db.DB in graphql ===" && grep -rn "db\.DB\." internal/graphql/ 2>/dev/null || echo "CLEAN"
	@echo "=== db.DB in REST handlers ===" && grep -rn "db\.DB\." internal/rest/handlers_*.go 2>/dev/null || echo "CLEAN"
	@echo "=== respond.Error(w, 422 ===" && grep -rn "respond\.Error(w, 422" internal/rest/ 2>/dev/null || echo "CLEAN"
	@echo "=== config.C.BaseURL in handlers ===" && grep -rn "config\.C\.BaseURL" internal/rest/handlers_*.go 2>/dev/null || echo "CLEAN"

# ─── Clean ────────────────────────────────────────────────────────────────────

.PHONY: clean
clean: stop ## Stop server and remove binary
	rm -f $(BINARY)
	@echo "✓ Cleaned"

.PHONY: clean-all
clean-all: clean ## Stop server and remove generated files
	rm -f cert.pem key.pem .env
	@if [ -d gitrepos/ ]; then \
		rm -rf gitrepos/ 2>/dev/null || { \
			if timeout 5 sudo -n true 2>/dev/null; then \
				sudo -n rm -rf gitrepos/; \
			else \
				echo "✗ Failed to remove gitrepos/ (permission denied, and sudo -n unavailable)"; \
				exit 1; \
			fi; \
		}; \
	fi
	@echo "✓ Cleaned all generated files"

.PHONY: test-clean-all
test-clean-all: clean-all test-db-stop ## Stop test DB and remove generated files

# ─── Help ─────────────────────────────────────────────────────────────────────

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help
