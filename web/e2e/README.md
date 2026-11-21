# Browser tests

Playwright tests of the flows that matter most: signing in (and the lockout
message), an administrator creating a VM from a plan, a customer powering their
VM, adding a firewall rule and taking a backup, staff role limits, viewing the
panel as a customer, and saving a setting.

They run against a real panel and web app with a throwaway PostgreSQL database
and a fake node agent (`cmd/fake-agent`), which answers the node API from memory
and touches no hypervisor. `e2e/stack.sh` starts all of it and Playwright stops
it, dropping the database, when the tests end. The fake agent is a separate
binary that no release or image contains.

## Running them

Needs Go, Node, a PostgreSQL server you can create databases on, its client
tools (`psql`, `createdb`, `dropdb`), and port 50051 free: the panel reaches
every node there.

```bash
cd web
npm ci
npx playwright install chromium   # or set E2E_BROWSER_CHANNEL=chrome to use an installed Chrome
PGHOST=127.0.0.1 PGUSER=postgres npm run test:e2e
```

By default the web app runs as the dev server. CI tests a production build
instead:

```bash
API_BASE_URL=http://127.0.0.1:18480 npm run build
E2E_WEB_MODE=start npm run test:e2e
```

| Variable | Default | |
|---|---|---|
| `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD` | `127.0.0.1`, `5432`, `postgres` | the server the throwaway database is made on |
| `E2E_WEB_MODE` | `dev` | `start` serves a build made with `API_BASE_URL` pointing at the test panel |
| `E2E_PANEL_PORT`, `E2E_WEB_PORT` | `18480`, `3480` | |
| `E2E_BROWSER_CHANNEL` | | `chrome` to use an installed Chrome |
| `E2E_LOG_DIR` | | a directory the panel and fake agent logs are copied to when the stack stops |

A failed test leaves a Playwright trace under `test-results/` (open it with
`npx playwright show-trace`). In CI a failed run uploads `test-results/`, with
the panel and fake agent logs in `test-results/stack-logs/`, as the
`e2e-failure-*` artifact.

The accounts and the customer VM the tests use are in `seed.sql` and
`fixtures.ts`. Tests share one panel and run one at a time; each leaves the
customer VM running, as it found it.
