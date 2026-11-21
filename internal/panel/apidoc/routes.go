package apidoc

import (
	"net/http"

	"github.com/maburvm/panel/internal/panel/handler"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// Hand-written shapes for answers built from maps in the handlers.
var (
	user     = models.User{}
	vmMetric = obj("cpu_percent", tNum, "memory_used", tInt, "memory_total", tInt, "memory_used_percent", tNum,
		"disk_read_bytes_per_sec", tInt, "disk_write_bytes_per_sec", tInt, "network_rx_bytes_per_sec", tInt, "network_tx_bytes_per_sec", tInt)
	idList     = obj("id", tStr)
	settingMap = js{"type": "object", "additionalProperties": tAny,
		"description": "Settings by section name; each section is an object of that section's fields. Stored secrets come back masked."}
	emailTemplate = service.EmailTemplate{}
	smtpTest      = obj("smtpHost", tStr, "smtpPort", tInt, "smtpUser", tStr, "smtpPassword", tStr, "smtpFrom", tStr,
		"smtpFromName", tStr, "enableTls", tBool, "to", tStr)
	signInBody = obj("email", tStr, "password", tStr)
)

func apiRoutes() []*apiRoute {
	var all []*apiRoute
	for _, g := range [][]*apiRoute{
		group("Health",
			get("/health", "Liveness").can("public").out(obj("status", tStr, "timestamp", tTime)).
				note("Process liveness only; dependencies are not checked. `/healthz` and `/livez` are the same."),
			get("/healthz", "Liveness (alias)").can("public").out(obj("status", tStr, "timestamp", tTime)),
			get("/livez", "Liveness (alias)").can("public").out(obj("status", tStr, "timestamp", tTime)),
			get("/readyz", "Readiness").can("public").
				out(obj("status", tStr, "timestamp", tTime, "dependencies", js{"type": "object", "additionalProperties": obj("status", tStr, "error", tStr)})).
				note("Pings the database and the job queue. 503 with `status: not_ready` while either is down. Nodes are not part of readiness."),
			get("/metrics", "Prometheus metrics").can("metrics").raw("text/plain").
				note("Prometheus text format: node state, usage, job queue, backups, pools and alerts. The metrics and example alert rules are in docs/DEPLOYMENT.md, section 14."),
		),
		group("Authentication",
			post("/api/v1/auth/login", "Sign in").can("public").limit().in(handler.LoginRequest{}).out(handler.LoginResponse{}).
				note("Sets the `accessToken` session cookie and returns the same token for `Authorization: Bearer`. With two-factor on, the first call answers `requires2fa: true`; repeat it with `totpCode`. After repeated failures the panel asks for `captchaToken` (see Settings, System, Security) and may lock the account for a while."),
			post("/api/v1/auth/register", "Self-registration").can("public").in(signInBody).out(tAny).
				note("Closed: answers 403. Accounts come from staff, the billing module or an import."),
			post("/api/v1/auth/forgot-password", "Ask for a password reset email").can("public").limit().
				in(obj("email", tStr, "captchaToken", tStr)).out(msg).
				note("Always answers the same, whether or not the account exists."),
			post("/api/v1/auth/reset-password", "Set a new password with a reset token").can("public").limit().
				in(obj("token", tStr, "password", tStr)).out(msg),
			post("/api/v1/auth/logout", "Sign out").can("signed-in").out(msg).
				note("Ends this session and clears the cookie."),
			get("/api/v1/auth/me", "The signed-in account").can("signed-in").
				out(ok(obj("id", tUUID, "email", tStr, "role", tStr, "two_factor_enabled", tBool, "two_factor_setup_required", tBool,
					"permissions", arr(tStr), "staff_role", tStr, "staff_role_name", tStr, "impersonator", obj("id", tStr, "email", tStr)))),
			post("/api/v1/auth/impersonation/end", "Stop viewing as a customer").can("signed-in").out(ok(obj("redirect", tStr))).
				note("Returns to the staff session that opened the view."),
			get("/api/v1/auth/client-ip", "The address the panel sees for this request").can("public").out(ok(obj("ip", tStr))),
			get("/api/v1/auth/oidc/config", "Whether single sign-on is offered").can("public").out(obj("enabled", tBool, "label", tStr)).
				note("Unlike most answers, not wrapped in `success`/`data`."),
			get("/api/v1/auth/oidc/login", "Start single sign-on").can("public").limit().code(http.StatusFound).
				note("Redirects to the OpenID Connect provider."),
			get("/api/v1/auth/oidc/callback", "Single sign-on return address").can("public").limit().code(http.StatusFound).
				note("The provider redirects here; the panel then redirects to the dashboard, to the two-factor step, or back to the sign-in page with `?sso=<reason>`."),
			post("/api/v1/auth/oidc/2fa", "Finish single sign-on with a two-factor code").can("public").limit().
				in(obj("ticket", tStr, "code", tStr)).out(handler.LoginResponse{}),
			get("/api/v1/auth/billing-sso", "One-time sign-in link from the billing system").can("public").limit().q("token").code(http.StatusFound).
				note("The link the billing module hands out (event `user.sso`): works once, for 60 seconds, customer accounts only."),
			get("/api/v1/auth/first-run", "Whether the first administrator can still be created").can("public").out(ok(obj("open", tBool))),
			post("/api/v1/auth/first-run", "Create the first administrator").can("public").limit().
				in(obj("token", tStr, "name", tStr, "email", tStr, "password", tStr)).out(ok(obj("email", tStr))).
				note("Needs the one-time token the installer printed. 409 once an administrator exists."),
		),
		group("Branding",
			get("/api/v1/branding", "The operator's name, colours and logo").can("public").out(ok(tAny)),
			get("/api/v1/branding/logo", "The logo as an image").can("public").raw("image/*").
				note("404 when no logo is set."),
			get("/api/v1/branding/login-image", "The sign-in page picture").can("public").raw("image/*"),
		),
		group("Account",
			get("/api/v1/settings/profile", "Own profile").can("signed-in").out(ok(user)),
			put("/api/v1/settings/profile", "Change own name or email").can("signed-in").
				in(obj("name", tStr, "email", tStr, "current_password", tStr)).out(ok(user)).
				note("A new email needs `current_password`."),
			post("/api/v1/settings/change-password", "Change own password").can("signed-in").
				in(obj("current_password", tStr, "new_password", tStr)).out(okMsg).
				note("Signs out every session; sign in again."),
			post("/api/v1/settings/2fa/setup", "Start two-factor setup").can("signed-in").
				out(ok(obj("secret", tStr, "otp_url", tStr, "qr_code_png", described(tStr, "Base64 PNG."), "backup_codes", arr(tStr)))).
				note("Two-factor is on only after `POST /api/v1/settings/2fa/verify`. The backup codes are shown this once; each signs in once in place of a code."),
			post("/api/v1/settings/2fa/verify", "Turn two-factor on").can("signed-in").in(obj("code", tStr)).out(okMsg),
			post("/api/v1/settings/2fa/disable", "Turn two-factor off").can("signed-in").in(obj("code", tStr)).out(okMsg),
			get("/api/v1/settings/sessions", "Own sessions").can("signed-in").out(okList(service.SessionView{})),
			del("/api/v1/settings/sessions/:id", "Sign out one of own sessions").can("signed-in").out(ok(nil)),
			post("/api/v1/settings/sessions/revoke-others", "Sign out every other session").can("signed-in").
				out(ok(obj("revoked", tInt, "token", tStr))).
				note("Answers a fresh token for this session."),
			get("/api/v1/settings/sign-ins", "Own sign-in history").can("signed-in").out(okList(models.SignInEvent{})),
			get("/api/v1/api-keys", "Own API keys").can("signed-in").out(obj("success", tTrue, "data", arr(models.APIKey{}), "api_enabled", tBool)),
			post("/api/v1/api-keys", "Create an API key").can("signed-in").in(service.CreateAPIKeyRequest{}).code(http.StatusCreated).
				out(ok(obj("token", tStr))).
				note("The `data` object is the key's record plus `token`, the secret `mvk_...` value, shown this once. A read-only or narrowed key cannot create keys."),
			del("/api/v1/api-keys/:id", "Revoke an API key").can("signed-in").out(okMsg),
			get("/api/v1/ssh-keys", "Own SSH keys").can("signed-in").out(okList(models.SSHKey{})),
			post("/api/v1/ssh-keys", "Add an SSH public key").can("signed-in").in(service.CreateSSHKeyRequest{}).code(http.StatusCreated).out(ok(models.SSHKey{})),
			post("/api/v1/ssh-keys/generate", "Generate an SSH key pair").can("signed-in").in(service.GenerateSSHKeyRequest{}).code(http.StatusCreated).
				out(ok(service.GeneratedSSHKey{})).note("The private key is returned once and not kept."),
			del("/api/v1/ssh-keys/:id", "Delete an SSH key").can("signed-in").out(okMsg),
			get("/api/v1/recipes", "Own first-boot scripts").can("signed-in").out(okList(models.Recipe{})),
			post("/api/v1/recipes", "Save a first-boot script").can("signed-in").in(service.RecipeRequest{}).code(http.StatusCreated).out(ok(models.Recipe{})),
			put("/api/v1/recipes/:id", "Change a first-boot script").can("signed-in").in(service.RecipeRequest{}).out(ok(models.Recipe{})),
			del("/api/v1/recipes/:id", "Delete a first-boot script").can("signed-in").out(okMsg),
			get("/api/v1/notifications", "Notifications").can("signed-in").
				out(obj("success", tTrue, "data", arr(service.UserNotification{}), "unread", tInt)).
				note("Customers get their notifications and the unread count. Staff with `audit:read` get the 20 newest audit log rows instead (`data` is then audit entries, with no `unread`)."),
			put("/api/v1/notifications/read-all", "Mark every notification read").can("signed-in").out(okOnly),
			put("/api/v1/notifications/:id/read", "Mark a notification read").can("signed-in").out(okOnly),
			get("/api/v1/quota", "Own resource limits and use").can("signed-in").out(ok(service.QuotaStatus{})),
			get("/api/v1/self-service", "What customers may do without the billing system").can("signed-in").out(ok(tAny)),
			get("/api/v1/search", "Search VMs, accounts and nodes").can("signed-in").q("q").
				out(ok(obj("vms", arr(handler.SearchVM{}), "users", arr(handler.SearchUser{}), "nodes", arr(handler.SearchNode{})))).
				note("Customers find only their own VMs."),
			get("/api/v1/system/version", "Panel build, and each node's agent build").can("signed-in").
				out(ok(obj("panel", tAny, "nodes", arr(tAny)))).note("Customers get the panel build only."),
		),
		group("VMs",
			get("/api/v1/vms", "List VMs").can("vm:read").cust().q("page", "page_size", "limit", "offset", "search", "status", "node_id", "user_id").
				out(described(obj("message", tStr, "data", arr(handler.VMListItem{}), "total", tInt, "page", tInt, "limit", tInt,
					"totalPages", tInt, "offset", tInt, "has_more", tBool), "Accepts page/page_size or limit/offset.")),
			post("/api/v1/vms", "Create a VM").can("vm:read vm:create").cust().in(handler.CreateVMRequest{}).code(http.StatusCreated).
				out(msgData(handler.CreateVMResponse{})).
				note("Creation runs as a job; follow it with `GET /api/v1/vms/{id}/operation`. Customers are held to their quota, plan and the self-service switches."),
			get("/api/v1/vms/:id", "Get a VM").can("vm:read").cust().q("include_status").out(msgData(handler.VMDetailResponse{})),
			put("/api/v1/vms/:id", "Change a VM (resize, rename, plan)").can("vm:read vm:update|billing:update").cust().
				in(handler.UpdateVMRequest{}).out(msgData(tAny)),
			del("/api/v1/vms/:id", "Delete a VM").can("vm:read vm:delete").cust().q("delete_imported_volumes").out(msg).
				note("Deletion runs as a job. Customers cannot delete a VM a billing service pays for unless the operator allows it."),
			get("/api/v1/vms/:id/operation", "Progress of the VM's current job").can("vm:read").cust().out(bare(tAny)),
			get("/api/v1/vms/:id/activity", "The VM's activity log").can("vm:read").cust().q("limit").out(bare(arr(models.AuditLog{}))),
			get("/api/v1/vms/:id/metrics", "Live usage").can("vm:read").cust().out(bare(vmMetric)).
				note("When the node does not answer, the allocated memory with zero usage."),
			get("/api/v1/vms/:id/metrics/history", "Recorded usage").can("vm:read").cust().q("minutes", "limit").out(okList(models.VMMetricSample{})),
			post("/api/v1/vms/:id/start", "Start").can("vm:read vm:power").cust().in(handler.LifecycleRequest{}).out(msgData(handler.LifecycleResponse{})),
			post("/api/v1/vms/:id/stop", "Shut down").can("vm:read vm:power").cust().in(handler.LifecycleRequest{}).out(msgData(handler.LifecycleResponse{})),
			post("/api/v1/vms/:id/force-stop", "Power off").can("vm:read vm:power").cust().in(handler.LifecycleRequest{}).out(msgData(handler.LifecycleResponse{})),
			post("/api/v1/vms/:id/restart", "Restart").can("vm:read vm:power").cust().in(handler.LifecycleRequest{}).out(msgData(handler.LifecycleResponse{})),
			post("/api/v1/vms/:id/suspend", "Suspend").can("vm:read vm:lifecycle").in(handler.LifecycleRequest{}).out(msgData(handler.LifecycleResponse{})).
				note("Customers cannot suspend or unsuspend; staff and the billing system do."),
			post("/api/v1/vms/:id/unsuspend", "Unsuspend").can("vm:read vm:lifecycle").in(handler.LifecycleRequest{}).out(msgData(handler.LifecycleResponse{})),
			post("/api/v1/vms/:id/rebuild", "Reinstall").can("vm:read vm:lifecycle").cust().in(handler.RebuildVMRequest{}).out(msgData(handler.RebuildVMResponse{})).
				note("Erases the disk and installs the chosen template, of any kind (Linux, RouterOS, Windows)."),
			post("/api/v1/vms/:id/reset-password", "Set the root or Administrator password").can("vm:read vm:lifecycle").cust().
				in(obj("password", tStr)).out(msgData(tAny)),
			post("/api/v1/vms/:id/iso/attach", "Attach an ISO").can("vm:read vm:lifecycle").cust().in(obj("iso_url", tStr)).out(msgData(jobQueued)).
				note("A customer's URL must be public; the node refuses private addresses and large files."),
			post("/api/v1/vms/:id/iso/detach", "Detach the ISO").can("vm:read vm:lifecycle").cust().out(msgData(jobQueued)),
			post("/api/v1/vms/:id/clone", "Clone").can("vm:read vm:lifecycle").cust().in(obj("hostname", tStr, "node_id", tStr)).
				code(http.StatusCreated).out(msgData(tAny)),
			post("/api/v1/vms/:id/rescue", "Boot into rescue mode").can("vm:read vm:rescue").cust().in(obj("iso_url", tStr)).out(msgData(jobQueued)).
				note("Without `iso_url`, the rescue ISO from System settings, Rescue."),
			post("/api/v1/vms/:id/unrescue", "Leave rescue mode").can("vm:read vm:rescue").cust().out(msgData(jobQueued)),
			get("/api/v1/vms/:id/disks", "Extra disks").can("vm:read vm:lifecycle").cust().out(okList(models.VMDisk{})),
			post("/api/v1/vms/:id/disks", "Add a disk").can("vm:read vm:lifecycle").cust().in(obj("size_gb", tInt, "pool_id", tStr)).
				code(http.StatusCreated).out(ok(models.VMDisk{})),
			del("/api/v1/vms/:id/disks/:device", "Detach and delete a disk").can("vm:read vm:lifecycle").cust().q("delete_volume").out(okMsg).
				note("The disk and its data are deleted."),
			get("/api/v1/vms/:id/vnc", "Open the console").can("vm:read vm:console").cust().out(msgData(handler.VNCConfigResponse{})).
				note("Returns a short-lived token and the websocket path (`/ws/vnc`)."),
			post("/api/v1/vms/:id/vnc/refresh", "Change the console password").can("vm:read vm:console").cust().out(msgData(tAny)),
			post("/api/v1/vms/:id/console/token", "Legacy console token").can("vm:read vm:console").cust().out(tAny).
				note("Retired: always 503. Use `GET /api/v1/vms/{id}/vnc`."),
			del("/api/v1/vms/:id/console/token", "Revoke a console token").can("vm:read vm:console").cust().in(obj("jti", tStr)).out(msg),
			post("/api/v1/vms/:id/console/enable", "Turn the console on").can("vm:read vm:console").cust().out(msgData(tAny)),
			post("/api/v1/vms/:id/console/disable", "Turn the console off").can("vm:read vm:console").cust().out(msgData(tAny)),
			post("/api/v1/vms/:id/console/repair", "Repair the console").can("vm:read vm:console").cust().q("confirm").out(msgData(tAny)).
				note("May restart the VM."),
			post("/api/v1/vms/:id/ssh/token", "Open the in-browser SSH console").can("vm:console").cust().out(ok(tAny)).
				note("Returns a short-lived token for the `/ws/ssh` websocket."),
			get("/api/v1/vms/:id/initial-password", "Whether the initial password is still kept").can("vm:read").cust().
				out(ok(obj("available", tBool, "expires_at", nullable(tTime)))),
			post("/api/v1/vms/:id/initial-password/reveal", "Reveal the initial password").can("vm:read vm:credentials").cust().
				out(ok(obj("password", tStr, "expires_at", nullable(tTime)))).
				note("Not available while staff view as a customer."),
			del("/api/v1/vms/:id/initial-password", "Forget the initial password").can("vm:read vm:credentials").cust().out(okOnly),
			put("/api/v1/vms/:id/limits", "Set CPU and disk limits").can("vm:read vm:update").in(obj("limits", models.VMLimits{})).
				out(ok(service.LimitsResult{})),
			post("/api/v1/vms/:id/limits/apply", "Apply the stored limits again").can("vm:read vm:update").out(ok(service.LimitsResult{})),
			post("/api/v1/vms/:id/throttle", "Throttle a VM").can("vm:read vm:update").in(tAny).out(ok(service.LimitsResult{})),
			del("/api/v1/vms/:id/throttle", "Lift a throttle").can("vm:read vm:update").out(ok(service.LimitsResult{})),
			post("/api/v1/vms/:id/migrate", "Move the VM to another node").can("vm:migrate").in(service.MigrationRequest{}).
				code(http.StatusAccepted).out(ok(models.VMMigration{})).
				note("409 with the check in `data` when the VM cannot move."),
			get("/api/v1/vms/:id/migration/check", "Check a move before starting it").can("vm:migrate").q("dest_node_id", "dest_pool_id", "mode").
				out(ok(service.MigrationCheck{})),
			get("/api/v1/vms/:id/migration", "The VM's latest move").can("vm:migrate").out(ok(models.VMMigration{})),
			post("/api/v1/vms/:id/migration/cancel", "Cancel a move").can("vm:migrate").out(ok(models.VMMigration{})),
		),
		group("VM networking",
			get("/api/v1/vms/:id/networks", "Network interfaces").can("vm:read").cust().out(msgData(arr(handler.NetworkResponse{}))),
			post("/api/v1/vms/:id/networks", "Add a network interface").can("vm:update").in(handler.AddNetworkRequest{}).
				code(http.StatusCreated).out(msgData(handler.NetworkResponse{})),
			post("/api/v1/vms/:id/ip-addresses", "Add an IPv4 address").can("vm:update").in(handler.AssignIPRequest{}).
				code(http.StatusCreated).out(msgData(tAny)).
				note("From a pool on the VM's node. The guest is reconfigured live when it can be, else at the next start from the panel."),
			del("/api/v1/vms/:id/ip-addresses/:network_id", "Release an address").can("vm:update").out(msg),
			post("/api/v1/vms/:id/ipv6", "Give the VM an IPv6 prefix").can("vm:update").in(obj("pool_id", tStr)).
				code(http.StatusCreated).out(msgData(tAny)),
			del("/api/v1/vms/:id/ipv6", "Release the VM's IPv6").can("vm:update").out(msg),
			put("/api/v1/vms/:id/networks/:network_id/bandwidth", "Set an interface's speed limit").can("vm:update").
				in(handler.SetBandwidthRequest{}).out(msg),
			put("/api/v1/vms/:id/networks/:network_id/vlan", "Set an interface's VLAN").can("vm:update").in(handler.SetVLANRequest{}).out(msg),
			put("/api/v1/vms/:id/networks/:network_id/anti-spoofing", "Turn anti-spoofing on or off").can("vm:update").
				in(obj("enabled", tBool)).out(msg),
			get("/api/v1/vms/:id/networks/:network_id/port-forwards", "Port forwards of an interface").can("vm:read").cust().
				out(msgData(arr(handler.PortForwardResponse{}))),
			post("/api/v1/vms/:id/networks/:network_id/port-forwards", "Add a port forward on an interface").can("vm:update").cust().
				in(handler.AddPortForwardRequest{}).code(http.StatusCreated).out(msgData(handler.PortForwardResponse{})),
			del("/api/v1/vms/:id/networks/:network_id/port-forwards/:forward_id", "Remove a port forward").can("vm:update").cust().out(msg),
			get("/api/v1/vms/:id/port-forwards", "Port forwards").can("vm:read").cust().out(msgData(arr(handler.PortForwardResponse{}))),
			post("/api/v1/vms/:id/port-forwards", "Add a port forward").can("vm:update").cust().in(handler.AddPortForwardRequest{}).
				code(http.StatusCreated).out(msgData(handler.PortForwardResponse{})).
				note("Published on the node's one forward address. A NAT VM forwards only ports inside its own block."),
			del("/api/v1/vms/:id/port-forwards/:forward_id", "Remove a port forward").can("vm:update").cust().out(msg),
			get("/api/v1/vms/:id/firewall-rules", "Firewall rules").can("vm:read").cust().out(msgData(arr(handler.FirewallRuleResponse{}))),
			post("/api/v1/vms/:id/firewall-rules", "Add a firewall rule").can("vm:update").cust().in(handler.AddFirewallRuleRequest{}).
				code(http.StatusCreated).out(msgData(handler.FirewallRuleResponse{})),
			del("/api/v1/vms/:id/firewall-rules/:rule_id", "Remove a firewall rule").can("vm:update").cust().out(msg),
			get("/api/v1/vms/:id/firewall-status", "Whether the node enforces the VM's firewall").can("vm:read").cust().
				out(ok(obj("enforcement", tStr))),
			put("/api/v1/vms/:id/firewall-default", "Inbound policy for traffic no rule matches").can("vm:update").cust().
				in(obj("default", js{"type": "string", "enum": []any{"accept", "drop"}})).out(ok(obj("firewall_default", tStr))),
			put("/api/v1/vms/:id/smtp", "Block or allow outbound mail (port 25)").can("vm:update").
				in(obj("blocked", tBool)).out(ok(obj("smtp_blocked", tBool))),
			del("/api/v1/vms/:id/guest-network-note", "Dismiss the guest network note").can("vm:update").cust().out(okOnly),
			get("/api/v1/vms/:id/firewall-groups", "Firewall groups on the VM").can("vm:read").cust().out(okList(service.AttachedFirewallGroup{})),
			post("/api/v1/vms/:id/firewall-groups", "Attach a firewall group").can("vm:update").cust().
				in(obj("group_id", tStr, "position", tStr)).code(http.StatusCreated).out(okMsg),
			del("/api/v1/vms/:id/firewall-groups/:group_id", "Detach a firewall group").can("vm:update").cust().out(okMsg),
			put("/api/v1/vms/:id/rdns", "Set reverse DNS for one of the VM's addresses").can("vm:update").cust().
				in(obj("ip_address", tStr, "rdns", tStr)).out(ok(obj("ip_address", tStr, "rdns", tStr))),
			get("/api/v1/vms/:id/nat", "NAT VM: public ports and hostnames").can("vm:read").cust().out(ok(service.VMNATInfo{})),
			post("/api/v1/vms/:id/hostnames", "Route a website hostname to a NAT VM").can("vm:update").cust().
				in(service.AddHostnameRequest{}).code(http.StatusCreated).out(ok(models.VMHostname{})),
			post("/api/v1/vms/:id/hostnames/:hostname_id/verify", "Check a hostname's TXT record or address").can("vm:update").cust().
				out(ok(models.VMHostname{})),
			del("/api/v1/vms/:id/hostnames/:hostname_id", "Stop routing a hostname").can("vm:update").cust().out(okMsg),
			get("/api/v1/vms/:id/bandwidth", "This month's transfer").can("vm:read").cust().q("node_id").out(handler.BandwidthUsageResponse{}).
				note("Not wrapped in an envelope."),
			get("/api/v1/vms/:id/bandwidth/history", "Transfer by month").can("vm:read").cust().out(obj("vm_id", tStr, "history", tAny)),
			put("/api/v1/vms/:id/bandwidth/quota", "Set the monthly transfer quota").can("vm:update").in(obj("quota_gb", tInt)).
				out(msgData(obj("quota_gb", tInt))),
		),
		group("Backups and snapshots",
			get("/api/v1/vms/:id/backups", "List backups").can("backup:read").cust().q("status", "limit", "offset").out(pageOffset(handler.BackupListItem{})),
			post("/api/v1/vms/:id/backups", "Take a backup").can("backup:create").cust().in(handler.CreateBackupRequest{}).
				code(http.StatusCreated).out(msgData(handler.CreateBackupResponse{})).
				note("Runs as a job. Customers are held to their plan's backup allowance."),
			get("/api/v1/vms/:id/backups/:backup_id", "Get a backup").can("backup:read").cust().out(msgData(handler.BackupListItem{})),
			del("/api/v1/vms/:id/backups/:backup_id", "Delete a backup").can("backup:delete").cust().
				out(obj("success", tTrue, "message", tStr, "file_not_deleted", tBool)).
				note("Deletes the file first. A backup another kept backup builds on cannot be deleted before the newer ones."),
			post("/api/v1/vms/:id/backups/:backup_id/restore", "Restore a backup over the VM").can("backup:update").cust().
				in(handler.RestoreBackupRequest{}).code(http.StatusAccepted).out(msgData(handler.RestoreBackupResponse{})).
				note("The VM must be stopped. An incremental backup is restored with every backup it builds on."),
			post("/api/v1/vms/:id/backups/:backup_id/restore-new", "Restore a backup as a new VM").can("admin:access").
				in(handler.RestoreAsNewRequest{}).code(http.StatusAccepted).out(obj("success", tTrue, "message", tStr, "data", tAny)).
				note("On any active node that passes the destination test; the original VM and the backup are unchanged."),
			get("/api/v1/vms/:id/backup-schedule", "The VM's first backup schedule").can("backup:read").cust().out(msgData(handler.ConfigureScheduleResponse{})),
			put("/api/v1/vms/:id/backup-schedule", "Set the VM's backup schedule").can("backup:update").cust().
				in(handler.ConfigureScheduleRequest{}).out(msgData(handler.ConfigureScheduleResponse{})),
			del("/api/v1/vms/:id/backup-schedule", "Remove the VM's backup schedule").can("backup:delete").cust().out(msg),
			get("/api/v1/vms/:id/backup-schedules", "Backup schedules").can("backup:read").cust().out(msgData(arr(handler.ConfigureScheduleResponse{}))),
			post("/api/v1/vms/:id/backup-schedules", "Add a backup schedule").can("backup:update").cust().
				in(handler.ConfigureScheduleRequest{}).out(msgData(handler.ConfigureScheduleResponse{})).
				note("Incremental backups (`full_every_days`) are for staff; customers cannot turn them on."),
			put("/api/v1/vms/:id/backup-schedules/:schedule_id", "Change a backup schedule").can("backup:update").cust().
				in(handler.ConfigureScheduleRequest{}).out(msgData(handler.ConfigureScheduleResponse{})),
			del("/api/v1/vms/:id/backup-schedules/:schedule_id", "Remove a backup schedule").can("backup:delete").cust().out(msg),
			get("/api/v1/vms/:id/backup-stats", "Backup totals for the VM").can("backup:read").cust().out(msgData(handler.BackupStatsResponse{})),
			get("/api/v1/backups/overview", "Backups across the fleet").can("backup:read").q("stale_days", "state", "node_id", "user_id", "q").
				out(ok(service.BackupOverview{})),
			get("/api/v1/vms/:id/snapshots", "List snapshots").can("vm:read").cust().q("status", "limit", "offset").out(pageOffset(handler.SnapshotListItem{})),
			post("/api/v1/vms/:id/snapshots", "Take a snapshot").can("vm:read snapshot:create").cust().in(handler.CreateSnapshotRequest{}).
				code(http.StatusCreated).out(msgData(handler.CreateSnapshotResponse{})),
			get("/api/v1/vms/:id/snapshots/:snapshot_id", "Get a snapshot").can("vm:read").cust().out(msgData(handler.SnapshotDetailResponse{})),
			post("/api/v1/vms/:id/snapshots/:snapshot_id/restore", "Revert to a snapshot").can("vm:read snapshot:update").cust().
				out(msgData(handler.RestoreSnapshotResponse{})),
			del("/api/v1/vms/:id/snapshots/:snapshot_id", "Delete a snapshot").can("vm:read snapshot:delete").cust().out(msg),
			get("/api/v1/images", "List images").can("vm:read").cust().out(bare(arr(models.Image{}))).
				note("Disk captures that outlive their VM and can seed a new one."),
			post("/api/v1/images", "Capture a VM's disk as an image").can("snapshot:create").cust().in(handler.CreateImageRequest{}).
				code(http.StatusAccepted).out(bare(models.Image{})),
			post("/api/v1/images/:id/retry", "Retry a failed capture").can("snapshot:create").cust().out(msgData(models.Image{})),
			del("/api/v1/images/:id", "Delete an image").can("snapshot:delete").cust().out(msg),
		),
		group("Networking",
			get("/api/v1/floating-ips", "List floating IPs").can("floating_ip:read").cust().out(okList(models.IPAddress{})),
			post("/api/v1/floating-ips", "Allocate a floating IP for an account").can("network:update").
				in(obj("pool_id", tStr, "user_id", tStr, "region", tStr)).code(http.StatusCreated).out(ok(models.IPAddress{})),
			post("/api/v1/floating-ips/order", "Order a floating IP").can("floating_ip:create").cust().in(obj("region", tStr)).
				code(http.StatusCreated).out(ok(models.IPAddress{})).
				note("`floating_ip:create` is not in the custom role catalog: staff need the Administrator role."),
			post("/api/v1/floating-ips/:id/attach", "Point a floating IP at a VM").can("floating_ip:update").cust().in(obj("vm_id", tStr)).out(ok(models.IPAddress{})),
			post("/api/v1/floating-ips/:id/detach", "Detach a floating IP").can("floating_ip:update").cust().out(ok(models.IPAddress{})),
			del("/api/v1/floating-ips/:id", "Release a floating IP").can("floating_ip:delete").cust().code(http.StatusNoContent),
			get("/api/v1/floating-ips/billing", "Floating IP counts for billing").can("floating_ip:read").cust().q("user_id").
				out(ok(obj("user_id", tStr, "total", tInt, "free", tInt, "billable", tInt))),
			get("/api/v1/vpcs", "List VPCs").can("vpc:read").cust().out(okList(models.ManagedNetwork{})),
			post("/api/v1/vpcs", "Create a VPC").can("vpc:create").cust().in(service.CreateVPCRequest{}).code(http.StatusCreated).out(ok(models.ManagedNetwork{})),
			get("/api/v1/vpcs/:id", "Get a VPC").can("vpc:read").cust().out(ok(models.ManagedNetwork{})),
			del("/api/v1/vpcs/:id", "Delete a VPC").can("vpc:delete").cust().code(http.StatusNoContent),
			get("/api/v1/firewall-groups", "List firewall groups").can("firewall_group:read").cust().q("scope").out(okList(service.FirewallGroupSummary{})).
				note("Customers see their own groups and the groups staff made for everyone."),
			post("/api/v1/firewall-groups", "Create a firewall group").can("firewall_group:update").cust().in(obj("name", tStr, "description", tStr, "position", tStr)).
				code(http.StatusCreated).out(ok(models.FirewallGroup{})),
			get("/api/v1/firewall-groups/:id", "Get a firewall group").can("firewall_group:read").cust().out(ok(service.FirewallGroupDetail{})),
			put("/api/v1/firewall-groups/:id", "Change a firewall group").can("firewall_group:update").cust().in(obj("name", tStr, "description", tStr, "position", tStr)).
				out(ok(models.FirewallGroup{})),
			del("/api/v1/firewall-groups/:id", "Delete a firewall group").can("firewall_group:update").cust().out(okMsg),
			post("/api/v1/firewall-groups/:id/rules", "Add a rule to a group").can("firewall_group:update").cust().in(handler.AddFirewallRuleRequest{}).
				code(http.StatusCreated).out(ok(models.FirewallGroupRule{})),
			del("/api/v1/firewall-groups/:id/rules/:rule_id", "Remove a rule from a group").can("firewall_group:update").cust().out(okMsg),
			get("/api/v1/regions", "List regions").can("signed-in").q("available").
				out(okList(obj("id", tStr, "slug", tStr, "name", tStr, "country", tStr, "enabled", tBool, "node_count", tInt))),
			post("/api/v1/regions", "Create a region").can("node:update").in(service.CreateRegionRequest{}).code(http.StatusCreated).out(ok(models.Region{})),
			put("/api/v1/regions/:id", "Change a region").can("node:update").in(service.CreateRegionRequest{}).out(ok(models.Region{})),
			del("/api/v1/regions/:id", "Delete a region").can("node:update").code(http.StatusNoContent),
			post("/api/v1/regions/:id/nodes/:node_id", "Put a node in a region").can("node:update").out(okOnly),
			get("/api/v1/ip-pools", "List IP pools").can("network:read").q("limit", "offset").out(okList(models.IPPool{})),
			post("/api/v1/ip-pools", "Create an IP pool").can("network:update").in(service.CreateIPPoolRequest{}).code(http.StatusCreated).out(ok(models.IPPool{})),
			get("/api/v1/ip-pools/:id", "Get an IP pool").can("network:read").out(ok(models.IPPool{})),
			put("/api/v1/ip-pools/:id", "Change an IP pool").can("network:update").in(service.UpdateIPPoolRequest{}).out(ok(models.IPPool{})),
			del("/api/v1/ip-pools/:id", "Delete an IP pool").can("network:update").code(http.StatusNoContent),
			get("/api/v1/ip-pools/:id/addresses", "Addresses in a pool").can("network:read").q("limit", "offset").out(okList(models.IPAddress{})),
			post("/api/v1/ip-pools/:id/addresses", "Add an address to a pool").can("network:update").in(service.CreateIPAddressRequest{}).
				code(http.StatusCreated).out(ok(models.IPAddress{})),
			post("/api/v1/ip-pools/:id/generate", "Fill a pool from its range").can("network:update").out(obj("success", tTrue, "message", tStr, "data", obj("count", tInt))),
			post("/api/v1/ip-pools/:id/allocate", "Allocate an address").can("network:update").in(service.AllocateIPAddressRequest{}).out(ok(models.IPAddress{})),
			get("/api/v1/ip-pools/:id/prefixes", "Delegated IPv6 prefixes").can("network:read").out(okList(service.DelegatedPrefix{})),
			get("/api/v1/ip-pools/:id/rdns-zone", "Reverse zone file for a pool").can("network:read").raw("text/plain"),
			post("/api/v1/ip-pools/:id/rdns-import", "Read reverse DNS from the nameserver").can("network:update").out(ok(obj("imported", tInt))),
			post("/api/v1/ip-pools/addresses/:address_id/release", "Release an address").can("network:update").code(http.StatusNoContent),
			put("/api/v1/ip-pools/addresses/:address_id/rdns", "Set an address's reverse DNS").can("network:update").in(obj("rdns", tStr)).out(ok(models.IPAddress{})),
			get("/api/v1/ip-history", "Who held an address, and when").can("network:read").q("address", "at").out(okList(models.IPAssignment{})).
				note("For abuse reports: give the address and, optionally, `at` (RFC 3339), the time of the report. Every holding of the address, or of an IPv6 prefix containing it, newest first (at most 500); with `at`, only the holdings in force then. `estimated` marks a holding already in place when the history began, so its start time is when recording started, not when the address was assigned."),
			get("/api/v1/networks", "List managed networks").can("network:read").out(okList(models.ManagedNetwork{})),
			post("/api/v1/networks", "Create a managed network").can("network:create").in(models.ManagedNetwork{}).code(http.StatusCreated).out(ok(models.ManagedNetwork{})),
			del("/api/v1/networks/:id", "Delete a managed network").can("network:delete").out(okMsg),
			get("/api/v1/dns/provider", "Whether a nameserver is connected").can("network:read").out(ok(tAny)),
			get("/api/v1/dns/zones", "List DNS zones").can("network:read").out(okList(models.DNSZone{})),
			post("/api/v1/dns/zones", "Create a DNS zone").can("network:update").in(service.ZoneRequest{}).code(http.StatusCreated).out(ok(models.DNSZone{})),
			get("/api/v1/dns/zones/:id", "Get a DNS zone").can("network:read").out(ok(models.DNSZone{})),
			put("/api/v1/dns/zones/:id", "Change a DNS zone").can("network:update").in(service.ZoneRequest{}).out(ok(models.DNSZone{})),
			del("/api/v1/dns/zones/:id", "Delete a DNS zone").can("network:update").out(okMsg),
			get("/api/v1/dns/zones/:id/export", "Zone file").can("network:read").raw("text/plain"),
			post("/api/v1/dns/zones/:id/sync", "Push a zone to the nameserver").can("network:update").out(okMsg),
			get("/api/v1/dns/zones/:id/records", "Records in a zone").can("network:read").out(okList(models.DNSRecord{})),
			post("/api/v1/dns/zones/:id/records", "Add a record").can("network:update").in(service.RecordRequest{}).code(http.StatusCreated).out(ok(models.DNSRecord{})),
			put("/api/v1/dns/records/:recordId", "Change a record").can("network:update").in(service.RecordRequest{}).out(ok(models.DNSRecord{})),
			del("/api/v1/dns/records/:recordId", "Delete a record").can("network:update").out(okMsg),
			get("/api/v1/abuse/guests", "Guests by new outbound connection rate").can("network:read").q("all").
				out(obj("success", tTrue, "data", arr(models.GuestConnection{}), "threshold", tInt)),
			post("/api/v1/abuse/guests/quarantine", "Cut or restore a guest's traffic").can("network:update").
				in(obj("node_id", tStr, "mac", tStr, "quarantined", tBool, "reason", tStr)).out(okOnly),
			get("/api/v1/ip-reputation", "Blocklist status of the address space").can("network:read").q("all").out(okList(models.IPReputation{})),
			post("/api/v1/ip-reputation/check", "Check the address space now").can("network:update").out(msgData(obj("checked", tInt))),
		),
		group("Nodes and storage",
			get("/api/v1/nodes", "List nodes").can("node:read").q("status", "limit", "offset").out(msgData(arr(handler.NodeListItem{}))),
			post("/api/v1/nodes", "Add a node").can("node:create").in(handler.RegisterRequest{}).code(http.StatusCreated).
				out(msgData(handler.RegisterResponse{})),
			get("/api/v1/nodes/:id", "Get a node").can("node:read").out(msgData(handler.NodeDetailResponse{})),
			put("/api/v1/nodes/:id", "Change a node").can("node:update").in(handler.UpdateNodeRequest{}).out(msgData(tAny)).
				note("Includes pausing new VMs (`creation_paused`) and maintenance."),
			del("/api/v1/nodes/:id", "Remove a node").can("node:delete").in(handler.DeleteNodeRequest{}).out(msg),
			post("/api/v1/nodes/:id/regenerate-token", "New agent token").can("node:update").out(msgData(handler.RegenerateTokenResponse{})),
			get("/api/v1/nodes/:id/token", "Reveal the agent token").can("node:update").out(msgData(obj("token", tStr))),
			get("/api/v1/nodes/:id/metrics", "Live node usage").can("node:read").out(msgData(tAny)),
			get("/api/v1/nodes/:id/metrics/history", "Recorded node usage").can("node:read").q("minutes", "limit").out(okList(models.NodeMetricSample{})),
			get("/api/v1/nodes/:id/preflight", "Last host check report").can("node:read").out(ok(service.NodePreflight{})),
			post("/api/v1/nodes/:id/preflight", "Run the host checks now").can("node:update").out(ok(service.NodePreflight{})),
			get("/api/v1/nodes/:id/agent-settings", "The agent's environment settings").can("node:read").out(ok(service.NodeAgentSettings{})).
				note("Secrets are reported only as set or not set."),
			get("/api/v1/nodes/:id/nat", "NAT port range of a node").can("node:read").out(ok(service.NodeNATSettings{})),
			put("/api/v1/nodes/:id/nat", "Set a node's NAT port range").can("node:update").in(obj("port_start", tInt, "port_end", tInt)).out(ok(service.NodeNATSettings{})),
			post("/api/v1/nodes/:id/install-command", "New installer command for a node").can("node:create").
				out(ok(obj("token", tStr, "expires_at", tTime, "script_url", tStr, "command", tStr))).
				note("Single use, one hour. Refused for a node an agent has answered for."),
			get("/api/v1/nodes/:id/drain", "Drain report").can("node:read").out(ok(obj("report", service.DrainReport{}, "link", tAny))),
			post("/api/v1/nodes/:id/drain/plan", "Plan a drain").can("node:drain").in(service.DrainRequest{}).out(ok(service.DrainPlan{})),
			post("/api/v1/nodes/:id/drain", "Start a drain").can("node:drain").in(service.DrainRequest{}).code(http.StatusAccepted).out(ok(models.NodeDrain{})),
			post("/api/v1/nodes/:id/drain/abort", "Stop a drain").can("node:drain").out(ok(models.NodeDrain{})),
			post("/api/v1/nodes/:id/import/sync", "Adopt VMs found on the node").can("vm:import").out(msgData(tAny)),
			get("/api/v1/nodes/:id/import/virtualizor/preview", "Preview VMs to import from this node's Virtualizor").can("vm:import").q("custom_path").
				out(ok(handler.PreviewVirtualizorResponse{})),
			post("/api/v1/nodes/:id/import/virtualizor", "Import VMs from this node's Virtualizor").can("vm:import").in(handler.ImportVirtualizorRequest{}).
				out(ok(handler.ImportVirtualizorResponse{})),
			post("/api/v1/system/nodes/:id/update-agent", "Update the node's agent to the panel's build").can("node:update").out(okMsg).
				note("One node at a time; the node verifies, test-runs and rolls back by itself."),
			get("/api/v1/nodes/agent-binary/checksum", "SHA-256 of the bundled agent").can("public").limit().q("arch").
				out(obj("success", tTrue, "arch", tStr, "algorithm", tStr, "digest", tStr)),
			get("/api/v1/nodes/agent-binary", "Download the agent").can("token").limit().q("arch").raw("application/octet-stream"),
			get("/api/v1/nodes/enroll", "Check an enrollment token").can("token").limit().out(ok(service.PendingEnrollment{})),
			post("/api/v1/nodes/enroll", "Enroll a node").can("token").limit().in(tAny).out(ok(tAny)).
				note("Uses the token up and returns the node's agent credential."),
			get("/api/v1/storage/pools", "List storage pools").can("node:read").out(okList(models.StoragePool{})),
			post("/api/v1/storage/pools", "Add a storage pool").can("storage:create").in(models.StoragePool{}).code(http.StatusCreated).out(ok(models.StoragePool{})),
			get("/api/v1/storage/pools/:id", "Get a storage pool").can("node:read").out(models.StoragePool{}).
				note("Not wrapped in an envelope."),
			put("/api/v1/storage/pools/:id", "Change a storage pool").can("storage:update").in(models.StoragePool{}).out(ok(models.StoragePool{})),
			del("/api/v1/storage/pools/:id", "Remove a storage pool").can("storage:delete").code(http.StatusNoContent),
			get("/api/v1/storage/pools/:poolId/volumes", "Volumes in a pool").can("node:read").out(bare(arr(models.StorageVolume{}))),
			post("/api/v1/storage/pools/:poolId/volumes", "Create a volume").can("storage:create").in(models.StorageVolume{}).
				code(http.StatusCreated).out(bare(models.StorageVolume{})),
			del("/api/v1/storage/pools/:poolId/volumes/:volumeId", "Delete a volume").can("storage:delete").code(http.StatusNoContent),
		),
		group("Templates and plans",
			get("/api/v1/templates", "List templates").can("signed-in").q("include_nodes").out(handler.SuccessResponse{}).
				note("`data` is a list of templates with, for staff asking `include_nodes`, their state on each node."),
			post("/api/v1/templates", "Add a template").can("template:update").in(handler.CreateTemplateRequest{}).code(http.StatusCreated).out(handler.SuccessResponse{}),
			post("/api/v1/templates/download-url", "Add a template from a URL").can("template:update").in(obj("url", tStr, "name", tStr)).out(handler.SuccessResponse{}),
			get("/api/v1/templates/:id", "Get a template").can("signed-in").q("include_nodes").out(handler.SuccessResponse{}),
			put("/api/v1/templates/:id", "Change a template").can("template:update").in(handler.UpdateTemplateRequest{}).out(handler.SuccessResponse{}),
			del("/api/v1/templates/:id", "Delete a template").can("template:update").out(handler.SuccessResponse{}),
			post("/api/v1/templates/:id/sync", "Copy a template to nodes").can("template:update").in(handler.SyncTemplateRequest{}).out(handler.SuccessResponse{}),
			get("/api/v1/plans", "List plans").can("signed-in").q("active").out(okList(models.Plan{})),
			post("/api/v1/plans", "Create a plan").can("plan:update").in(service.PlanRequest{}).code(http.StatusCreated).out(ok(models.Plan{})),
			get("/api/v1/plans/:id", "Get a plan").can("signed-in").out(ok(models.Plan{})),
			put("/api/v1/plans/:id", "Change a plan").can("plan:update").in(service.PlanRequest{}).out(ok(models.Plan{})).
				note("VMs on the plan keep what they have until the change is applied with `POST /api/v1/plans/{id}/apply`."),
			del("/api/v1/plans/:id", "Delete a plan").can("plan:update").out(okMsg),
			get("/api/v1/plans/:id/apply", "Preview applying a plan to its VMs").can("plan:update vm:read").out(ok(service.PlanRollout{})),
			post("/api/v1/plans/:id/apply", "Apply a plan to its VMs").can("plan:update vm:update").out(ok(service.PlanRollout{})).
				note("Running VMs keep their size until they are stopped."),
		),
		group("Customers and staff",
			get("/api/v1/users", "List accounts").can("user:read").q("page", "page_size", "search", "role").out(pageNum(user)),
			post("/api/v1/users", "Create an account").can("user:read user:create").
				in(obj("name", tStr, "email", tStr, "password", tStr, "role", js{"type": "string", "enum": []any{"client", "admin"}},
					"ip_whitelist", arr(tStr), "send_welcome_email", tBool, "staff_role", tStr)).
				code(http.StatusCreated).out(bare(user)).
				note("Only the Owner creates staff accounts (`role: admin`)."),
			get("/api/v1/users/:id", "Get an account").can("user:read").out(bare(user)),
			put("/api/v1/users/:id", "Change an account").can("user:read user:update").
				in(obj("name", tStr, "email", tStr, "role", tStr, "ip_whitelist", arr(tStr))).out(ok(user)).
				note("Another staff account is the Owner's to change."),
			del("/api/v1/users/:id", "Delete an account").can("user:read user:delete").q("to", "vms").out(ok(tAny)).
				note("An account that owns VMs needs `to=<account id>` (give them away) or `vms=delete`."),
			post("/api/v1/users/:id/suspend", "Suspend an account").can("user:read user:update").in(obj("with_vms", tBool, "reason", tStr)).out(ok(tAny)),
			post("/api/v1/users/:id/unsuspend", "Unsuspend an account").can("user:read user:update").in(obj("with_vms", tBool)).out(ok(tAny)),
			post("/api/v1/users/:id/reset-password", "Reset a password").can("user:read user:recover").
				in(obj("method", js{"type": "string", "enum": []any{"email", "temporary"}})).
				out(ok(obj("sent_to", tStr, "temporary_password", tStr))),
			post("/api/v1/users/:id/reset-2fa", "Turn off an account's two-factor").can("user:read user:recover").out(ok(obj("two_factor_enabled", tBool))),
			post("/api/v1/users/:id/unlock", "Unlock an account after failed sign-ins").can("user:read user:recover").out(ok(obj("locked", tBool))),
			get("/api/v1/users/:id/sessions", "An account's sessions").can("user:read").out(okList(service.SessionView{})),
			del("/api/v1/users/:id/sessions/:sid", "Sign out one session").can("user:read user:recover").out(ok(nil)),
			post("/api/v1/users/:id/sessions/revoke-all", "Sign out every session").can("user:read user:recover").out(ok(nil)),
			get("/api/v1/users/:id/sign-ins", "An account's sign-in history").can("user:read").out(okList(models.SignInEvent{})).
				note("Another staff account's history is the Owner's to see."),
			put("/api/v1/users/:id/staff-role", "Give a staff account a role").can("user:read staff:manage").in(obj("role", tStr)).
				out(ok(obj("staff_role", tStr, "staff_role_name", tStr))),
			post("/api/v1/users/:id/impersonate", "View as a customer").can("user:read user:impersonate").browser().
				out(ok(obj("redirect", tStr, "expires_at", tTime))).
				note("Opens the customer's client area for up to an hour in this browser. Customer accounts only."),
			get("/api/v1/users/:id/quota", "An account's limits and use").can("user:read").out(ok(service.QuotaStatus{})),
			put("/api/v1/users/:id/quota", "Set an account's limits").can("user:update").in(service.SetQuotaRequest{}).out(ok(models.UserQuota{})),
			get("/api/v1/staff-roles", "Staff roles and the permission catalog").can("staff").
				out(ok(obj("areas", arr(middleware.PermissionArea{}), "roles", arr(middleware.StaffRole{}), "can_manage", tBool))).
				note("Each role also carries `members`, the number of staff holding it."),
			post("/api/v1/staff-roles", "Create a custom role").can("staff:manage").in(obj("name", tStr, "description", tStr, "permissions", arr(tStr))).
				out(ok(middleware.StaffRole{})),
			put("/api/v1/staff-roles/:id", "Change a custom role").can("staff:manage").in(obj("name", tStr, "description", tStr, "permissions", arr(tStr))).
				out(ok(middleware.StaffRole{})),
			del("/api/v1/staff-roles/:id", "Delete a custom role").can("staff:manage").out(ok(nil)),
			get("/api/v1/security/support-access", "Open support accounts").can("admin:access").out(okList(handler.SupportGrant{})),
			post("/api/v1/security/support-access", "Open a temporary support account").can("owner").
				in(obj("hours", tInt, "note", tStr, "role", tStr)).code(http.StatusCreated).
				out(ok(obj("grant", handler.SupportGrant{}, "password", tStr))).
				note("The password is shown once."),
			del("/api/v1/security/support-access/:id", "Close a support account").can("admin:access").out(ok(tAny)),
		),
		group("Operations",
			get("/api/v1/dashboard/stats", "Fleet totals").can("staff").out(bare(tAny)),
			get("/api/v1/dashboard/overview", "Dashboard overview").can("staff").out(ok(tAny)),
			get("/api/v1/dashboard/vms", "VM table for the dashboard").can("vm:read").q("search", "status", "node", "sort", "page", "page_size").
				out(ok(obj("vms", arr(tAny), "total", tInt, "page", tInt, "page_size", tInt, "all", tInt, "by_status", tAny))).
				note("Staff only: customers get 403."),
			get("/api/v1/dashboard/mine", "A customer's VMs at a glance").can("vm:read").cust().out(ok(obj("vms", arr(tAny)))),
			get("/api/v1/events/vm-status", "VM status stream").can("vm:read").cust().raw("text/event-stream").
				note("Server-sent events: a snapshot of every visible VM's status, then each change."),
			get("/api/v1/audit-logs", "Audit log").can("audit:read").q("page", "page_size", "action", "user_id", "resource_type", "start_date", "end_date").
				out(pageNum(models.AuditLog{})),
			get("/api/v1/alerts", "Alerts").can("ops:read").q("page", "page_size", "state").pageInList("alerts", models.Alert{}),
			post("/api/v1/alerts/:id/close", "Close an alert").can("ops:update").out(ok(obj("closed", tBool))),
			get("/api/v1/queue", "Job queue").can("ops:read").q("state").
				out(ok(obj("counts", arr(obj("state", tStr, "count", tInt)), "jobs", arr(handler.QueueJob{}), "workers", js{"type": "object", "additionalProperties": tInt},
					"saved_workers", js{"type": "object", "additionalProperties": tInt}))),
			put("/api/v1/queue/workers", "Set workers per queue").can("settings:update").
				in(js{"type": "object", "additionalProperties": tInt, "description": "Workers by queue: critical, default, batch, audit. Applies after a panel restart."}).out(ok(tAny)),
			post("/api/v1/queue/jobs/:id/retry", "Retry a job").can("ops:update").out(ok(tAny)),
			post("/api/v1/queue/jobs/:id/cancel", "Cancel a job").can("ops:update").out(ok(tAny)),
			get("/api/v1/security/checks", "Security centre checks").can("settings:read").out(okList(handler.SecurityCheck{})),
			get("/api/v1/setup", "First steps checklist").can("staff").out(ok(tAny)),
			put("/api/v1/setup/dismissed", "Hide the first steps checklist").can("settings:update").in(obj("dismissed", tBool)).out(ok(tAny)),
			get("/api/v1/export/:kind", "Export as CSV").can("admin:access").raw("text/csv").
				note("`kind` is `vms`, `users`, `ip-addresses` or `audit-log`."),
			get("/api/v1/billing/deliveries", "Events sent to the billing system").can("billing:read").q("page", "page_size", "status").
				pageInList("deliveries", service.WebhookDelivery{}),
			post("/api/v1/billing/deliveries/:id/retry", "Send an event again").can("billing:update").out(ok(obj("queued", tBool))),
			get("/api/v1/import/sources", "Panels to import from").can("import:manage").out(okList(service.VirtualizorSource{})),
			post("/api/v1/import/sources", "Add a panel to import from").can("import:manage").in(service.VirtualizorSourceInput{}).
				code(http.StatusCreated).out(ok(service.VirtualizorSource{})),
			put("/api/v1/import/sources/:id", "Change an import source").can("import:manage").in(service.VirtualizorSourceInput{}).out(ok(service.VirtualizorSource{})),
			del("/api/v1/import/sources/:id", "Remove an import source").can("import:manage").out(ok(nil)),
			post("/api/v1/import/sources/:id/test", "Test an import source").can("import:manage").out(ok(service.SourceTestResult{})),
			post("/api/v1/import/sources/:id/preview", "Read the source and plan an import").can("import:manage").in(tAny).out(ok(service.ImportPlan{})),
			get("/api/v1/import/sources/:id/preview", "The latest import plan").can("import:manage").out(ok(service.ImportPlan{})),
			post("/api/v1/import/sources/:id/apply", "Import the chosen items").can("import:manage").in(obj("items", arr(service.ImportItemRef{}))).
				out(ok(obj("results", arr(service.ImportApplyResult{})))),
			post("/api/v1/import/sources/:id/invites", "Invite the imported customers").can("import:manage").out(ok(obj("sent", tInt))),
			post("/api/v1/mcp", "Model Context Protocol endpoint").can("api-key").in(tAny).out(obj("jsonrpc", tStr, "id", tAny, "result", tAny, "error", tAny)).
				note("JSON-RPC 2.0 over Streamable HTTP for AI agents. A read-only key lists only the monitoring tools."),
			get("/api/v1/mcp", "MCP stream (not offered)").can("api-key").code(http.StatusMethodNotAllowed).out(tAny),
			get("/api/v1/openapi.json", "This document").can("public").out(tAny),
		),
		group("Settings",
			get("/api/v1/settings/system", "System settings").can("settings:read").out(ok(settingMap)),
			put("/api/v1/settings/system", "Save system settings").can("settings:update").in(settingMap).out(okMsg).
				note("Branding, email templates, backup destinations, alerts, the DNS provider and panel settings have their own endpoints."),
			get("/api/v1/settings/system/panel", "Panel settings and where each value comes from").can("settings:read").out(ok(arr(service.PanelSettingStatus{}))),
			put("/api/v1/settings/system/panel", "Save panel settings").can("settings:update staff:manage").
				in(js{"type": "object", "additionalProperties": nullable(tStr), "description": "Setting key to value; null or empty returns it to the environment or default."}).
				out(ok(arr(service.PanelSettingStatus{}))),
			put("/api/v1/settings/system/branding", "Save branding").can("settings:update").in(service.Branding{}).out(ok(tAny)),
			get("/api/v1/settings/system/bandwidth", "Transfer quota settings").can("settings:read").out(ok(tAny)),
			put("/api/v1/settings/system/bandwidth", "Save transfer quota settings").can("settings:update").in(service.BandwidthSettings{}).out(ok(service.BandwidthSettings{})),
			get("/api/v1/settings/system/rescue", "The rescue ISO in force").can("settings:read").out(ok(obj("iso_url", tStr, "source", tStr))),
			get("/api/v1/settings/system/alerts", "Alert delivery settings").can("settings:read").out(ok(service.AlertSettingsStatus{})),
			put("/api/v1/settings/system/alerts", "Save alert delivery settings").can("settings:update").in(service.AlertSettingsInput{}).out(ok(service.AlertSettingsStatus{})),
			post("/api/v1/settings/system/alerts/test", "Send a test alert").can("settings:update").in(tAny).out(ok(obj("results", arr(service.AlertChannelResult{})))),
			get("/api/v1/settings/system/email-templates", "Email templates").can("settings:read").out(ok(tAny)),
			put("/api/v1/settings/system/email-templates/:key", "Save an email template").can("settings:update").in(emailTemplate).out(ok(emailTemplate)),
			del("/api/v1/settings/system/email-templates/:key", "Return a template to the default").can("settings:update").out(okOnly),
			post("/api/v1/settings/system/email-templates/:key/preview", "Preview a template").can("settings:update").in(emailTemplate).out(ok(service.RenderedEmail{})),
			post("/api/v1/settings/system/email-templates/:key/test", "Send a template to an address").can("settings:update").
				in(obj("to", tStr, "template", emailTemplate)).out(okMsg),
			post("/api/v1/settings/system/email/test", "Test the mail server").can("settings:update").in(smtpTest).out(okMsg),
			get("/api/v1/settings/system/emails", "Email log").can("settings:read").q("page", "page_size", "status").pageInList("emails", service.EmailLogEntry{}),
			post("/api/v1/settings/system/emails/:id/retry", "Send an email again").can("settings:update").out(ok(obj("queued", tBool))),
			get("/api/v1/settings/system/dns-provider", "Nameserver connection").can("settings:read").out(ok(service.DNSProviderStatus{})),
			put("/api/v1/settings/system/dns-provider", "Save the nameserver connection").can("settings:update").
				in(obj("url", tStr, "server_id", tStr, "api_key", tStr)).out(ok(service.DNSProviderStatus{})),
			del("/api/v1/settings/system/dns-provider", "Remove the nameserver connection").can("settings:update").out(ok(service.DNSProviderStatus{})),
			post("/api/v1/settings/system/dns-provider/test", "Test a nameserver connection").can("settings:update").
				in(obj("url", tStr, "server_id", tStr, "api_key", tStr)).out(okMsg),
			get("/api/v1/settings/system/backup-destinations", "Backup destinations").can("settings:read").out(okList(service.BackupDestinationView{})),
			post("/api/v1/settings/system/backup-destinations", "Add a backup destination").can("settings:update").in(service.BackupDestinationInput{}).
				code(http.StatusCreated).out(ok(service.BackupDestinationView{})),
			put("/api/v1/settings/system/backup-destinations/:id", "Change a backup destination").can("settings:update").
				in(service.BackupDestinationInput{}).out(ok(service.BackupDestinationView{})),
			post("/api/v1/settings/system/backup-destinations/:id/default", "Send new backups to this destination").can("settings:update").out(okMsg),
			del("/api/v1/settings/system/backup-destinations/:id", "Remove a backup destination").can("settings:update").out(okMsg),
			post("/api/v1/settings/system/backup-destinations/test", "Test a destination from the panel and nodes").can("settings:update").
				in(obj("id", tStr, "node_ids", arr(tStr))).out(ok(obj("results", arr(service.BackupDestinationTestResult{})))),
			post("/api/v1/settings/system/backup-destinations/scan-host-key", "Read an SFTP server's host key").can("settings:update").
				in(obj("host", tStr, "port", tInt)).out(ok(obj("host_key", tStr, "fingerprint", tStr))),
		),
		group("Billing webhook",
			post("/webhooks/billing", "Billing system events").can("webhook").in(handler.WebhookPayload{}).out(handler.WebhookResponse{}).
				note("Signed requests from the billing system. `event` is one of `user.ensure`, `user.suspend`, `user.unsuspend`, `user.sso`, `vm.create`, `vm.status`, `vm.suspend`, `vm.unsuspend`, `vm.destroy`, `vm.resize`, `vm.reset_password`, `vm.usage`, `vm.start`, `vm.stop`, `vm.restart` or `catalog.list`. "+
					"Each signed request is accepted once (a repeat answers 409), and `timestamp` must be within 5 minutes of the panel's clock. An `X-Idempotency-Key` header makes a retried `vm.create` or `vm.destroy` answer with the stored result for 24 hours instead of acting twice. "+
					"503 until the key and secret are set. `GET /webhooks/billing/docs` lists each event's fields."),
			get("/webhooks/billing/docs", "Billing webhook reference").can("public").out(handler.WebhookDocumentation{}),
			get("/install-agent.sh", "Node installer script").can("public").limit().raw("text/x-shellscript"),
		),
	} {
		all = append(all, g...)
	}
	return all
}

// pageInList sets a 200 answer of items nested under data[key].
func (r *apiRoute) pageInList(key string, item any) *apiRoute { return r.out(pageIn(key, item)) }

var apiTagDescriptions = map[string]string{
	"Health":                "Probes for orchestrators and monitoring.",
	"Authentication":        "Signing in and out, single sign-on and the first administrator.",
	"Branding":              "What the sign-in page shows before anyone signs in.",
	"Account":               "What any signed-in account does with itself: profile, two-factor, sessions, keys, scripts, notifications.",
	"VMs":                   "Virtual machines: lifecycle, power, console, credentials, limits and moves.",
	"VM networking":         "A VM's addresses, interfaces, port forwards, firewall, reverse DNS, NAT hostnames and transfer.",
	"Backups and snapshots": "Backups, schedules, snapshots and images.",
	"Networking":            "Floating IPs, VPCs, firewall groups, regions, IP pools, DNS, abuse and reputation.",
	"Nodes and storage":     "Hypervisor nodes, their installer and agent, drains and storage pools.",
	"Templates and plans":   "Operating system templates and the plans customers order.",
	"Customers and staff":   "Accounts, staff roles, impersonation and support access.",
	"Operations":            "Dashboard, audit log, alerts, job queue, exports, billing deliveries, imports and MCP.",
	"Settings":              "System settings: panel, branding, quotas, alerts, email, DNS and backup destinations.",
	"Billing webhook":       "The signed webhook a billing system drives the panel through, and the node installer.",
	"Webhooks":              "Requests the panel sends to you.",
}

var apiWebhooks = map[string]apiWebhook{
	"billingEvent": {
		summary: "Event for the billing system",
		desc: "Sent to the webhook URL under System settings, API, when a VM is created, fails to install, is suspended, unsuspended or destroyed, " +
			"when it goes over its transfer quota, and when an account's floating IP count changes. Headers: `X-Webhook-Event` (the event), " +
			"`X-Webhook-Id` (the same as `id`, for de-duplication) and, with a secret saved, `X-Webhook-Signature`: the hex HMAC-SHA256 of the raw body, no prefix. " +
			"Each event is stored and retried up to 10 times over about four hours; the outcome is under Billing deliveries.",
		body: obj(
			"id", described(tStr, "Delivery id; repeats on a retry."),
			"event", js{"type": "string", "enum": []any{service.BillingEventVMCreated, service.BillingEventVMFailed, service.BillingEventVMSuspended,
				service.BillingEventVMUnsuspended, service.BillingEventVMDestroyed, service.BillingEventOverage, service.BillingEventFloatingIP}},
			"sent_at", tTime,
			"vm_id", described(tStr, "VM events."),
			"user_id", tStr, "user_ref", described(tStr, "The billing system's client reference, when known."),
			"hostname", tStr, "status", tStr, "external_ref", described(tStr, "The billing system's service reference from vm.create."),
			"plan_id", tStr, "ip", tStr, "ips", arr(tStr), "username", tStr,
			"reason", described(tStr, "vm.failed: why; floating_ip.usage: what changed."),
			"retrying", described(tBool, "vm.failed: the panel is still trying."),
			"node_id", tStr, "used_gb", tNum, "quota_gb", tNum, "period_start", tTime, "period_end", tTime,
			"total", described(tInt, "floating_ip.usage"), "free", tInt, "billable", tInt, "address", tStr,
		),
	},
	"alert": {
		summary: "Operator alert",
		desc: "Sent to the alert webhook (Alerts page) in the JSON format; Slack and Discord formats post a line of text instead. " +
			"With a signing secret, `X-Webhook-Signature` is the hex HMAC-SHA256 of the raw body. `status` is `firing`, `resolved` or `event`; " +
			"`kind` is `node_offline`, `node_silent`, `storage_pool`, `firewall_off`, `backup_failed`, `job_failed` or `network_drift`. Fields may be added; none are renamed or removed.",
		body: service.AlertWebhookPayload{},
	},
}

const apiOverview = `The panel's REST API, as the web app and the billing module use it.

## Base address

Every path is relative to the panel's address, for example ` + "`https://panel.example.com/api/v1/vms`" + `. The production setup publishes only the web app, which passes ` + "`/api`" + `, ` + "`/ws`" + `, ` + "`/webhooks`" + ` and ` + "`/install-agent.sh`" + ` to the panel.

## Signing in

- **Session cookie.** ` + "`POST /api/v1/auth/login`" + ` sets ` + "`accessToken`" + ` (HttpOnly, SameSite=Lax). A browser write carrying it that the browser marks cross-site or same-site is refused.
- **Bearer token.** The same login answers ` + "`token`" + `; send ` + "`Authorization: Bearer <token>`" + `. It lasts the session length under Settings, System, Security.
- **API key.** Make one under Settings, API keys and send ` + "`X-API-Key: mvk_...`" + ` or ` + "`Authorization: Bearer mvk_...`" + `. A key acts as its owner, with the owner's staff role as it is at each call. A **read-only** key may only GET (MCP excepted); an **own resources only** key makes a staff owner act as a customer. Neither kind may manage API keys or account settings. Operators can switch API keys off (Settings, System, API).

Two-factor, IP whitelists and suspension apply to every method. While "Require two-factor authentication for administrators" is on (Settings, System, Security), staff without two-factor get ` + "`403 two_factor_required`" + ` until they set it up (API keys excepted).

## Who may call what

Each operation states its access as ` + "`x-access`" + ` and in words:

- ` + "`public`" + `, ` + "`signed-in`" + ` (any account, acting on itself), ` + "`staff`" + ` (any staff account);
- otherwise the staff permissions it needs, all of them, ` + "`a|b`" + ` meaning either. The catalog is ` + "`GET /api/v1/staff-roles`" + `. ` + "`admin:access`" + ` is the Administrator and Owner roles.

` + "`x-customer: true`" + ` means customers may call it too, on their own resources; another tenant's resource answers 404, not 403. Staff viewing as a customer act as that customer and cannot touch the customer's password, email, two-factor, sessions, API keys or initial passwords.

## Answers

Successful answers are JSON objects, but not yet one shape; making them one is planned and will come with a release note. Each operation documents its own. Most are ` + "`{\"success\": true, \"data\": ...}`" + `; some older ones are ` + "`{\"message\": \"...\", \"data\": ...}`" + ` without ` + "`success`" + `, ` + "`{\"data\": ...}`" + ` alone, or the object itself. Lists come four ways:

| Shape | Used by |
|---|---|
| ` + "`data`" + ` array, ` + "`total`" + `, ` + "`limit`" + `, ` + "`offset`" + `, ` + "`has_more`" + ` | backups, snapshots; VMs add ` + "`page`" + ` and ` + "`totalPages`" + ` and also accept ` + "`page`" + `/` + "`page_size`" + ` |
| ` + "`data`" + ` array, ` + "`total`" + `, ` + "`page`" + `, ` + "`page_size`" + `, ` + "`totalPages`" + ` | accounts, audit log |
| ` + "`data: {<items>: [...], total, page, page_size}`" + ` | alerts, billing deliveries, email log |
| ` + "`data`" + ` array, everything at once | the rest |

Errors carry ` + "`error`" + ` and usually ` + "`message`" + `, sometimes a machine ` + "`code`" + `; read ` + "`message`" + ` when present. Writes that start work on a node run as background jobs and answer at once (often 202 or a job id); poll the resource.

## Rate limits

Every signed-in account and every API key gets "API requests per minute" (Settings, System, API; default 1200, 0 turns it off), with ten seconds' worth allowed at once. Past it the panel answers 429 with ` + "`Retry-After`" + `. Sign-in, password reset, single sign-on, first-run and the node installer endpoints are limited per client address (one request every two seconds on average, ten at once). The billing webhook is not counted. Counts are kept in the panel's memory.

## Webhooks

The panel receives signed events from a billing system at ` + "`POST /webhooks/billing`" + ` and sends two kinds of its own, described under webhooks: billing events, and operator alerts.

## Stability

This is version 1. Fields may be added to answers; existing ones are not renamed or removed without a note in the release.`
