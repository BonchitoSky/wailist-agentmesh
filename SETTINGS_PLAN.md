# Settings page — competitor research + implementation plan

Branch: `feature/settings-page` · Base: `upstream/master` (`7ec483b`)

AgentMesh has no settings page. The `Settings` item in the account menu
(`Topbar.tsx:203`) is a no-op whose only `onClick` is `setMenuState("closed")`,
and there is no `/settings` route.

Every claim about our own code below was checked against `master` at `7ec483b`.
Where a capability does **not** exist, that is stated plainly rather than assumed.

---

## 1. What comparable platforms put in Settings

Surveyed n8n (the closest analogue), plus Zapier, Activepieces, and Windmill.

| Section                | n8n                                                                     | Zapier                          | Activepieces           | Windmill                                     |
| ---------------------- | ----------------------------------------------------------------------- | ------------------------------- | ---------------------- | -------------------------------------------- |
| **Personal / account** | name, email, password, MFA, theme, personalisation                      | My Profile; Security and data   | Profile                | Account settings                             |
| **Usage & plan**       | execution quota, licence                                                | Plan & billing, task usage      | Platform billing       | —                                            |
| **Members & roles**    | Owner / Admin / Member + custom roles                                   | Teams / Enterprise Admin Center | Team                   | Users — members, permissions                 |
| **Credentials**        | Credentials (shared); External Secrets (Vault, AWS/GCP)                 | Connected accounts              | Connections; Pieces    | Encryption — per-workspace key               |
| **API access**         | public API keys                                                         | Developer / API keys            | API Keys               | User tokens, scoped permissions              |
| **Notifications**      | Log Streaming                                                           | email prefs — errors, digests   | Project Alerts         | Slack/Teams; Error Handler                   |
| **Environments**       | source control (git)                                                    | —                               | Git sync, signing keys | Git Sync; Dev workspace; Protection Rulesets |
| **SSO**                | SAML, LDAP                                                              | SAML SSO                        | SSO                    | SSO                                          |
| **Per-workflow**       | error workflow, timezone, execution-save policy, timeout, caller policy | Zap error handling              | —                      | Error handler, webhook                       |

Sources: [n8n workflow settings](https://docs.n8n.io/build/manage-workflows/configure-workflow-settings),
[n8n user management](https://docs.n8n.io/deploy/host-n8n/configure-n8n/user-management),
[n8n external secrets](https://docs.n8n.io/external-secrets/),
[n8n log streaming](https://docs.n8n.io/log-streaming/),
[Zapier profile](https://help.zapier.com/hc/en-us/articles/8496294243981-Manage-your-Zapier-account-profile),
[Zapier notification preferences](https://help.zapier.com/hc/en-us/articles/8496277613325-Manage-your-Zapier-account-email-notification-preferences),
[Zapier 2FA](https://help.zapier.com/hc/en-us/articles/8496305453069-Set-up-two-factor-authentication-for-your-Zapier-account),
[Activepieces project alerts](https://community.activepieces.com/t/introducing-project-alerts/4310),
[Activepieces manage pieces](https://www.activepieces.com/docs/admin-guide/guides/manage-pieces),
[Windmill workspace settings](https://www.windmill.dev/docs/core_concepts/workspace_settings),
[Windmill user tokens](https://www.windmill.dev/docs/core_concepts/user_tokens).

### The pattern worth copying

Two things stand out, and both are about restraint:

1. **Every setting on those pages changes runtime behaviour.** None are stored
   preferences that nothing reads. n8n's timezone setting exists because the
   Schedule Trigger reads it; its error-workflow setting exists because the
   executor reads it.
2. **The sections that dominate are exactly what a spending platform needs:**
   credentials, spend and quota limits, failure notifications, and API access.

### The pattern not to copy

Members/roles, SSO, environments, and git sync are multi-tenancy and
enterprise-deployment features. AgentMesh has no `memberships` table and no
workspace concept separate from a user. A "Members" section here would mean
building multi-tenancy — a project, not a settings tab.

---

## 2. What our codebase can actually support

### 2.1 Already real — no backend work needed

| Capability             | Evidence                                                                        |
| ---------------------- | ------------------------------------------------------------------------------- |
| Display name, org      | `users.name`, `users.org_name` (migration `000014_user_profile`)                |
| Read + write profile   | `GET /auth/me`, `PATCH /auth/me` → `UpdateProfile` (`router.go`, `auth.go:180`) |
| Member since           | `users.created_at`, already selected by `GetUserByID` (`store.go:397`)          |
| Credit balance         | `users.credit_balance_usd_micros`, `GET /credits/balance`                       |
| Tendril credit balance | `users.tendril_credit_usd_micros` (migration `000017`), `GET /tendril/credits`  |
| Public trigger URL     | `POST /run/{workflowId}` (`router.go:28`) — live, and undiscoverable in the UI  |

`created_at` is selected by the store but **not returned** by `Me` — the handler
builds its response map by hand and omits it.

### 2.2 Settings-shaped state that exists but has no editor

- **Low-balance threshold.** `AutoRecharge.thresholdUSD` (`lib/credits/types.ts:18`),
  default `$5` (`store.ts:15`). Genuinely read by `LowBalanceBanner.tsx:9` and
  `CanvasPage.tsx:501`, and duplicated as a second hardcoded `LOW_BALANCE_USD = 5`
  in `billing/page.tsx:19`. No screen can change it.
- **Key mode** (`byok` | `platform`). `WorkflowNode.KeyMode` (`models/types.go:74`),
  enforced in `provider.go:59`. Set per node in the Inspector (`Inspector.tsx:656`);
  there is no account default.

### 2.3 Things that look like settings but would be lies

Recorded because the obvious plan includes all three, and all three are wrong.

- **Auto-recharge on/off.** `autoRecharge.enabled` is read in exactly one place:
  `LowBalanceBanner.tsx:30`, where it swaps the banner text to "Auto-recharge is on."
  There is no recharge job, no scheduler, and no stored payment mandate — Cashfree
  orders are created interactively per checkout. A toggle labelled "auto-recharge"
  claims the platform will top the user up, and it never will.
  **Ship the threshold; do not ship the toggle.**
- **Default network.** `ALGORAND_NETWORK` is one deployment-wide env var read at
  boot (`main.go:59`) to construct the wallet service, mirrored to the frontend as
  `NEXT_PUBLIC_ALGORAND_NETWORK` (`Topbar.tsx:10`). The platform runs one chain, so
  a per-user network setting cannot be honoured. **Do not ship it.**
- **Default webhook URL.** `workflows.notify_url` exists in `000001_init.up.sql`
  and has **zero references in Go outside the migration** — never read, never
  written. Storing an account default would persist a URL nothing ever calls.
  Shipping it means first building run-completion webhooks (outbound POST, SSRF
  guard, retry/backoff). **Out of scope; do not render the field.**

### 2.4 Genuinely blocked

- **No platform email.** Email exists only as a workflow _node_ using the user's own
  provider key (`engine/nodes/action.go`). Alerting (`internal/alert`) posts to
  Discord from server env config. Per-user "email me on failure" needs new infra.
- **No token revocation.** Auth is a 7-day HS256 JWT in an HttpOnly cookie
  (`tokenTTL`, `auth.go:79`). No refresh token, no session table, no blacklist —
  **"sign out everywhere" is not implementable** without a design change.
- **No `oauth_identities` table.** The OAuth callback matches on verified email only
  (`GetOrCreateOAuthUser`, `store.go:421`). Connected accounts can't be listed or
  unlinked. PR #42 adds _connector_ OAuth but stores tokens in the node's `Secrets`
  map and adds no migration, so it does not change this.
- **No memberships or roles.** See §1.
- **`tool_credentials` is dead.** Created in `000001_init.up.sql`, referenced nowhere
  else in Go. Connector secrets live in the workflow graph JSONB
  (`WorkflowNode.Secrets`, encrypted via `secrets.go`). An account-level credential
  vault means migrating that model, not adding an endpoint.

---

## 3. What we ship

Four sections. Every item changes real behaviour.

### Account

- Display name → `PATCH /auth/me` (exists)
- Organisation name → same
- Email — read-only
- Member since — needs `createdAt` added to the `Me` response
- **Change password** → new `POST /auth/password`, verifies the old one, and
  refuses OAuth-only accounts (`password_hash == ""`) rather than letting them set
  one silently

### Billing & credits

- **Low balance alert threshold** — becomes server-authoritative, and collapses the
  duplicate `LOW_BALANCE_USD = 5` in `billing/page.tsx:19` onto the stored value
- Credit balance and Tendril credit balance — read-only, with links to top up
- Links to purchase history and `/refund-policy`

### Execution & safety

- **Per-call spend ceiling.** The server enforces only a global
  `MaxSingleX402QuoteUSDMicros` = $1,000/call (`models/types.go:376`). A user-set
  ceiling below it is a real safety control for autonomous agents, and it is
  enforceable at one chokepoint: `Runner.preflightCheck` (`runner.go:99`), which
  already has `r.store` and `wf.UserID` and is called before every spend path
  (`runner.go:723,728,850,883,920,951`).
- **Default key mode** (`byok` | `platform`) — applied to newly created Provider
  nodes; the per-node control in the Inspector still wins.

### Developer

- The public trigger endpoint, with a copy button. Live today and completely
  undiscoverable. Zero backend work.

### Explicitly deferred, and why

Rendered nowhere — an empty or disabled section is worse than no section.

| Deferred                          | Blocker                                                |
| --------------------------------- | ------------------------------------------------------ |
| Auto-recharge toggle              | No recharge job, no payment mandate (§2.3)             |
| Default network                   | Deployment-wide, not per-user (§2.3)                   |
| Default webhook URL               | `notify_url` is dead; needs the feature first (§2.3)   |
| Email notifications               | No platform email (§2.4)                               |
| Sign out everywhere / sessions    | No revocation (§2.4)                                   |
| Connected accounts                | No `oauth_identities` table (§2.4)                     |
| Members, roles, invites           | No `memberships` table (§1)                            |
| Account-level credential vault    | `tool_credentials` unused; secrets are per-node (§2.4) |
| 2FA, change email, delete account | Each needs its own design                              |

Delete account in particular: `credit_ledger` and `debit_ledger` are append-only
financial records and **must not be cascade-deleted**. That needs an
anonymise-not-delete design.

---

## 4. Schema

`000020_user_settings.up.sql` — `000019` is the highest on `master`.

```sql
CREATE TABLE IF NOT EXISTS user_settings (
    user_id                    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    low_balance_usd_micros     BIGINT NOT NULL DEFAULT 5000000,
    max_call_spend_usd_micros  BIGINT,
    default_key_mode           TEXT   NOT NULL DEFAULT 'byok',
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_settings_key_mode_valid CHECK (default_key_mode IN ('byok', 'platform')),
    CONSTRAINT user_settings_low_balance_non_negative CHECK (low_balance_usd_micros >= 0),
    CONSTRAINT user_settings_max_call_spend_positive
        CHECK (max_call_spend_usd_micros IS NULL OR max_call_spend_usd_micros > 0)
);
```

Money as integer micros, matching `credit_ledger` / `debit_ledger` exactly —
**never floats for currency**. `max_call_spend_usd_micros` is nullable, meaning
"no user ceiling, global cap only"; nullable columns scan into `*int64`, the
established pgx idiom in this codebase (`store.go:39,61,92`).

The CHECK constraints are the point: they make an invalid value a database error
rather than something handler code has to remember to prevent — the same approach
`000017`'s `tendril_credit_non_negative` already takes.

---

## 5. Endpoints

| Method  | Path             | Notes                                                                              |
| ------- | ---------------- | ---------------------------------------------------------------------------------- |
| `GET`   | `/settings`      | Returns defaults when no row exists — never 404                                    |
| `PATCH` | `/settings`      | Partial update; validates key mode and ceiling > 0 and ≤ the global cap            |
| `POST`  | `/auth/password` | `{currentPassword, newPassword}`; ≥ 8 chars; 401 wrong current; 400 for OAuth-only |

All inside the authed `r.Group` in `router.go`.

---

## 6. Commit order

Backend first, so the page reads something real.

| #   | Commit                                                        |
| --- | ------------------------------------------------------------- |
| 1   | `docs: research and plan the settings page`                   |
| 2   | `Add user_settings table`                                     |
| 3   | `Add user settings store methods`                             |
| 4   | `Add GET/PATCH /settings endpoints`                           |
| 5   | `Return createdAt from /auth/me`                              |
| 6   | `Add POST /auth/password`                                     |
| 7   | `Enforce the per-user call spend ceiling in the runner`       |
| 8   | `Add settings client to the frontend API layer`               |
| 9   | `Add settings page shell and route`                           |
| 10  | `Add account section to settings`                             |
| 11  | `Add billing section and server-backed low balance threshold` |
| 12  | `Add execution and developer sections to settings`            |
| 13  | `Link the Settings menu item to the settings page`            |

Reuse rather than rebuild: `Card` / `Pill` / `Hairline` / `ghostBtnSm` from
`components/ui/index.tsx`; the `bill-grid` 900px breakpoint and `panelStyle` from
`billing/page.tsx:34`; `respond.JSON` / `respond.Error`; the `handlers_test`
httptest pattern in `auth_test.go`. `/settings` goes in both `PROTECTED` and
`config.matcher` in `middleware.ts`.

Prettier over every touched JS/TS/CSS/MD file, `gofmt` over every touched Go file,
and formatting never shares a commit with logic.

---

## 7. Verification

**Automated**

- `cd backend && go test ./...`
- `cd frontend && npm run typecheck && npm run lint && npm run test`
- Migration applied **up and down** against a scratch database

**Manual — the acceptance criteria**

1. `GET /settings` for a user with no row returns defaults, not a 404.
2. `PATCH /settings` rejects a ceiling above `MaxSingleX402QuoteUSDMicros` and an
   unknown key mode.
3. Wrong current password → 401; OAuth-only account → 400 with a clear message.
4. Set the threshold to $50 → `LowBalanceBanner`, the canvas low-balance indicator,
   and `billing/page.tsx` all agree.
5. Set a $0.05 call ceiling → a workflow whose tool quotes more fails preflight with
   a clear error rather than spending.
6. `/settings` unauthenticated redirects to `/signin?next=/settings`.
7. Browser-verified live before commit, at desktop and 375px.

---

## 8. Risks

| Risk                                                     | Mitigation                                                                          |
| -------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| A stored limit the engine ignores is worse than no limit | The ceiling lands in `preflightCheck` in the same PR as its editor                  |
| PR #47 rewrites `Topbar.tsx` and adds `lib/nav.ts`       | Our nav change is one line at `Topbar.tsx:203`; it re-applies to `AppNav` trivially |
| Migration number collision                               | `000020` verified free at branch time; re-check immediately before merge            |
| Scope creep into multi-tenancy                           | Members/roles/SSO are explicitly out (§3)                                           |
