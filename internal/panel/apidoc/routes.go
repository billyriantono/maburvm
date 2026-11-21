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
			put("/api/v1/settings/profile", "Change own profile").can("signed-in").
				in(obj("name", tStr, "phone", tStr, "company", tStr, "timezone", tStr, "locale", tStr, "email", tStr, "current_password", tStr)).out(ok(user)).
				note("Each field left out keeps its value. `timezone` is an IANA name such as `Asia/Jakarta`; `locale` a language code such as `en` or `id-ID`. A new email needs `current_password`."),
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
			put("/api/v1/settings/email-preferences", "Whether to get marketing email").can("signed-in").in(obj("marketing", tBool)).
				out(ok(obj("marketing", tBool))).note("Service notices are sent either way."),
			get("/api/v1/api-keys", "Own API keys").can("signed-in").out(obj("success", tTrue, "data", arr(models.APIKey{}), "api_enabled", tBool)),
			post("/api/v1/api-keys", "Create an API key").can("signed-in").in(service.CreateAPIKeyRequest{}).code(http.StatusCreated).
				out(ok(obj("token", tStr))).
				note("The `data` object is the key's record plus `token`, the secret `mvk_...` value, shown this once. A read-only, narrowed or address-limited key cannot create keys. "+
					"`scope` narrows the key within its owner's role: `full` (the default), `read_only` (GET only), `vm_power` (list VMs, read one, start, stop, force-stop, restart) "+
					"or `billing` (billing deliveries, plans, customer accounts, usage reports and transfer; never staff accounts or roles). `allowed_ips` lists the addresses or CIDR ranges it may be used from "+
					"(a request from anywhere else answers 403 `api_key_address`); `expires_at` must be in the future."),
			del("/api/v1/api-keys/:id", "Revoke an API key").can("signed-in").out(okMsg),
			get("/api/v1/ssh-keys", "Own SSH keys").can("signed-in").q("account_id").out(okList(models.SSHKey{})),
			post("/api/v1/ssh-keys", "Add an SSH public key").can("signed-in").q("account_id").in(service.CreateSSHKeyRequest{}).code(http.StatusCreated).out(ok(models.SSHKey{})),
			post("/api/v1/ssh-keys/generate", "Generate an SSH key pair").can("signed-in").q("account_id").in(service.GenerateSSHKeyRequest{}).code(http.StatusCreated).
				out(ok(service.GeneratedSSHKey{})).note("The private key is returned once and not kept."),
			del("/api/v1/ssh-keys/:id", "Delete an SSH key").can("signed-in").q("account_id").out(okMsg),
			get("/api/v1/recipes", "Own first-boot scripts and the app catalog").can("signed-in").q("account_id").out(okList(models.Recipe{})).
				note("The caller's own recipes and every published one (`is_public`). `inputs` are the values asked for when ordering; the script names each as `{{key}}`."),
			post("/api/v1/recipes", "Save a first-boot script").can("signed-in").q("account_id").in(service.RecipeRequest{}).code(http.StatusCreated).out(ok(models.Recipe{})).
				note("`is_public` publishes it to every customer; only staff with `template:update` may, and it is ignored for anyone else."),
			put("/api/v1/recipes/:id", "Change a first-boot script").can("signed-in").q("account_id").in(service.RecipeRequest{}).out(ok(models.Recipe{})).
				note("Own recipes; staff with `template:update` also change published ones."),
			del("/api/v1/recipes/:id", "Delete a first-boot script").can("signed-in").q("account_id").out(okMsg),
			get("/api/v1/notifications", "Notifications").can("signed-in").q("limit", "offset").
				out(obj("success", tTrue, "meta", pageMeta, "data", arr(service.UserNotification{}), "unread", tInt, "total", tInt)).
				note("Customers get their notifications (30 newest unless `limit`, up to 100, and `offset` say otherwise), the unread count and the total. Staff with `audit:read` get the 20 newest audit log rows instead (`data` is then audit entries, with no `unread` or `meta`)."),
			put("/api/v1/notifications/read-all", "Mark every notification read").can("signed-in").out(okOnly),
			put("/api/v1/notifications/:id/read", "Mark a notification read").can("signed-in").out(okOnly),
			get("/api/v1/quota", "Own resource limits and use").can("signed-in").q("account_id").out(ok(service.QuotaStatus{})),
			get("/api/v1/self-service", "What customers may do without the billing system").can("signed-in").out(ok(tAny)),
			get("/api/v1/search", "Search VMs, accounts and nodes").can("signed-in").q("q").
				out(ok(obj("vms", arr(handler.SearchVM{}), "users", arr(handler.SearchUser{}), "nodes", arr(handler.SearchNode{})))).
				note("Customers find only their own VMs."),
			get("/api/v1/system/version", "Panel build, and each node's agent build").can("signed-in").
				out(ok(obj("panel", tAny, "nodes", arr(tAny)))).note("Customers get the panel build only."),
		),
		group("VMs",
			get("/api/v1/vms", "List VMs").can("vm:read").cust().q("page", "page_size", "limit", "offset", "search", "status", "node_id", "user_id", "tag", "project_id", "account_id").
				out(described(obj("success", tTrue, "message", tStr, "data", arr(handler.VMListItem{}), "meta", pageMeta, "total", tInt, "page", tInt, "limit", tInt,
					"totalPages", tInt, "offset", tInt, "has_more", tBool), "Accepts page/page_size (or per_page) or limit/offset.")),
			post("/api/v1/vms", "Create a VM").can("vm:read vm:create").cust().in(handler.CreateVMRequest{}).code(http.StatusCreated).
				out(msgData(handler.CreateVMResponse{})).
				note("Creation runs as a job; follow it with `GET /api/v1/vms/{id}/operation`. Customers are held to their quota, plan and the self-service switches. "+
					"`account_id` orders into a team account the caller operates in (its quota counts), `project_id` puts the VM in one of that account's projects, "+
					"`ipv6` adds a routed IPv6 prefix where the location has one (409 where it has not), and `recipe_inputs` fill the recipe's `{{key}}` placeholders."),
			get("/api/v1/vms/:id", "Get a VM").can("vm:read").cust().q("include_status").out(msgData(handler.VMDetailResponse{})),
			put("/api/v1/vms/:id", "Change a VM (resize, rename, plan)").can("vm:read vm:update|billing:update").cust().
				in(handler.UpdateVMRequest{}).out(msgData(tAny)),
			del("/api/v1/vms/:id", "Delete a VM").can("vm:read vm:delete").cust().q("delete_imported_volumes").out(msg).
				note("Deletion runs as a job. Customers cannot delete a VM a billing service pays for unless the operator allows it."),
			get("/api/v1/vms/:id/operation", "Progress of the VM's current job").can("vm:read").cust().out(bare(tAny)),
			get("/api/v1/vms/:id/activity", "The VM's activity log").can("vm:read").cust().q("limit").out(bare(arr(models.AuditLog{}))),
			get("/api/v1/vms/:id/metrics", "Live usage").can("vm:read").cust().out(bare(vmMetric)).
				note("When the node does not answer, the allocated memory with zero usage."),
			get("/api/v1/vms/:id/metrics/history", "Recorded usage").can("vm:read").cust().q("minutes", "limit").out(okList(models.VMMetricSample{})).
				note("`minutes` reaches back two years: the last 7 days from the raw samples, then hourly averages for the graph history kept (90 days unless changed under Customer area settings), then daily averages."),
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
				in(obj("password", tStr)).out(msgData(service.VMResetPasswordResponse{})).
				note("A running VM takes it through its guest agent. A stopped Linux VM has it written onto its disk (`method: offline`) and stays stopped; a stopped router takes it at its next start. Windows takes one only while it runs."),
			post("/api/v1/vms/:id/iso/attach", "Attach an ISO").can("vm:read vm:lifecycle").cust().
				in(obj("iso_id", tStr, "iso_url", tStr, "template_id", tStr, "drivers", tBool, "slot", tInt)).out(msgData(jobQueued)).
				note("`iso_id` mounts one of the ISOs staff publish (`GET /api/v1/portal`); `template_id` one from the ISOs page (for a customer, one offered on the VM's plan); `drivers` the drivers ISO staff chose, in drive 2. A customer's own `iso_url` must be public, and is refused (403 `iso_url_not_allowed`) when the operator allows only the published ones. `slot` is the CD drive: 1, the install drive that boots first (the default), or 2. A running VM takes the disc at once when its node's agent can, in a drive it already has; otherwise stop it first."),
			post("/api/v1/vms/:id/iso/detach", "Detach the ISO").can("vm:read vm:lifecycle").cust().in(obj("slot", tInt)).out(msgData(jobQueued)).
				note("Empties drive `slot` (1 by default); the drive stays, so a disc can go back in while the VM runs."),
			post("/api/v1/vms/:id/clone", "Clone").can("vm:read vm:lifecycle").cust().in(obj("hostname", tStr, "node_id", tStr)).
				code(http.StatusCreated).out(msgData(tAny)),
			post("/api/v1/vms/:id/rescue", "Boot into rescue mode").can("vm:read vm:rescue").cust().
				in(obj("iso_url", tStr, "mode", tStr, "ssh_key_ids", arr(tStr))).out(msgData(service.RescueResult{})).
				note("The VM must be stopped. Without `iso_url` it boots the built-in rescue system when System settings, Rescue, names one and the VM's node can run it (`mode: iso` asks for the rescue ISO instead): a fresh rescue system on the VM's own address, its disks attached, with a one-time `root_password` (shown once) and the chosen saved SSH keys. Otherwise the rescue ISO from those settings."),
			post("/api/v1/vms/:id/unrescue", "Leave rescue mode").can("vm:read vm:rescue").cust().out(msgData(jobQueued)),
			get("/api/v1/vms/:id/disks", "Extra disks").can("vm:read vm:lifecycle").cust().out(okList(models.VMDisk{})),
			post("/api/v1/vms/:id/disks", "Add a disk").can("vm:read vm:lifecycle").cust().in(obj("size_gb", tInt, "pool_id", tStr)).
				code(http.StatusCreated).out(ok(models.VMDisk{})),
			del("/api/v1/vms/:id/disks/:device", "Detach and delete a disk").can("vm:read vm:lifecycle").cust().q("delete_volume").out(okMsg).
				note("The disk and its data are deleted."),
			post("/api/v1/vms/:id/disks/:device/resize", "Grow a disk").can("vm:read vm:lifecycle").cust().in(obj("size_gb", tInt)).
				out(ok(service.DiskResizeResult{})).
				note("Grows `vda` (the system disk; staff only, a customer's grows with the plan) or an extra disk, whether the VM runs or not; disks never shrink. `live` says the running VM saw the new size at once. The guest still grows its partition and filesystem itself (`note`)."),
			post("/api/v1/vms/:id/disks/:device/move", "Move a disk to another pool of its node").can("vm:read vm:migrate").
				in(obj("dest_pool_id", tStr)).code(http.StatusAccepted).out(ok(service.VMDiskMove{})).
				note("Copied while the VM is off (it cannot start until the copy is done), or block-copied while it runs; then the VM uses the copy and the old disk is removed (a volume MaburVM did not make is kept and listed under orphaned disks). Directory and LVM pools both work. Refused while the VM has snapshots."),
			get("/api/v1/vms/:id/disk-moves", "The VM's disk moves").can("vm:read vm:migrate").out(okList(service.VMDiskMove{})),
			post("/api/v1/vms/:id/disk-moves/cancel", "Cancel a disk move").can("vm:read vm:migrate").out(ok(service.VMDiskMove{})).
				note("The disk stays where it was."),
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
			put("/api/v1/vms/:id/hardware", "Change a VM's hardware").can("vm:read vm:update").in(obj("hardware", models.Hardware{})).
				out(ok(service.HardwareResult{})).
				note("CPU model, sockets and threads, dedicated cores (taken from the node's pool), NUMA placement, huge pages, watchdog and restart on crash. The node saves them in the VM's definition and the VM uses them from its next power off and on (`restart_required`); nothing is restarted. Firmware, Secure Boot and the TPM are set when a VM is created. `warnings` say what the hardware means for moving the VM."),
			post("/api/v1/vms/:id/throttle", "Throttle a VM").can("vm:read vm:update").in(tAny).out(ok(service.LimitsResult{})),
			del("/api/v1/vms/:id/throttle", "Lift a throttle").can("vm:read vm:update").out(ok(service.LimitsResult{})),
			post("/api/v1/vms/:id/migrate", "Move the VM to another node").can("vm:migrate").in(service.MigrationRequest{}).
				code(http.StatusAccepted).out(ok(models.VMMigration{})).
				note("409 with the check in `data` when the VM cannot move."),
			get("/api/v1/vms/:id/migration/check", "Check a move before starting it").can("vm:migrate").q("dest_node_id", "dest_pool_id", "mode").
				out(ok(service.MigrationCheck{})),
			get("/api/v1/vms/:id/migration", "The VM's latest move").can("vm:migrate").out(ok(models.VMMigration{})),
			post("/api/v1/vms/:id/migration/cancel", "Cancel a move").can("vm:migrate").out(ok(models.VMMigration{})),
			put("/api/v1/vms/:id/lock", "Lock or unlock the VM against deletion and reinstall").can("vm:read vm:update").cust().
				in(obj("locked", tBool)).out(ok(obj("delete_lock", js{"type": "string", "enum": []any{"", "owner", "staff"}}))).
				note("A locked VM cannot be deleted or reinstalled until it is unlocked. An owner's lock does not stop the billing system removing the VM; a staff lock stops everything, and only staff lift it."),
			put("/api/v1/vms/:id/tags", "Set the VM's tags").can("vm:read vm:update").cust().in(obj("tags", arr(tStr))).out(ok(obj("tags", arr(tStr)))).
				note("Up to 10 lower-case labels of 1 to 32 letters, digits, dots, dashes, underscores or colons. Filter the VM list with `tag`."),
			get("/api/v1/vms/:id/notes", "Staff notes on the VM").can("vm:read").out(okList(service.StaffNote{})).
				note("Staff only; customers never see them."),
			post("/api/v1/vms/:id/notes", "Add a staff note to the VM").can("vm:read vm:update").in(obj("body", tStr)).
				code(http.StatusCreated).out(ok(service.StaffNote{})),
			put("/api/v1/vms/:id/notes/:note_id", "Change a staff note on the VM").can("vm:read vm:update").in(obj("body", tStr)).out(ok(service.StaffNote{})),
			del("/api/v1/vms/:id/notes/:note_id", "Delete a staff note on the VM").can("vm:read vm:update").out(ok(nil)),
			post("/api/v1/vms/bulk", "Act on several VMs at once").can("vm:power|vm:lifecycle|vm:update|vm:migrate").in(handler.BulkRequest{}).
				out(ok(obj("action", tStr, "preview", tBool, "results", arr(handler.BulkResult{}), "ok", tInt, "total", tInt))).
				note("`action` is start, stop, restart (needs `vm:power`), suspend, unsuspend (`vm:lifecycle`), lock, unlock (`vm:update`), apply_plan (`vm:update` and `billing:update`, with `plan_id`), add_firewall_group (`vm:update` and `firewall_group:read`, with `group_id`) or migrate (`vm:migrate`, with an optional `node_id`). At most 100 VMs. With `preview` nothing changes and each result says what would happen; moves are planned together, so two VMs are not both sent to room only one fits in."),
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
			del("/api/v1/vms/:id/ipv6", "Release the VM's IPv6").can("vm:update").cust().out(msg).
				note("Customers may turn their own VM's IPv6 off; they get it back through an address request."),
			get("/api/v1/vms/:id/ipv6/addresses", "Further IPv6 addresses in the VM's prefix").can("vm:read").cust().out(ok(service.VMIPv6Addresses{})),
			put("/api/v1/vms/:id/ipv6/addresses", "Set the further IPv6 addresses in the VM's prefix").can("vm:update").cust().
				in(handler.SetIPv6AddressesRequest{}).out(ok(service.VMIPv6Addresses{})).
				note("Addresses inside the VM's routed prefix, besides its `::1`, up to 16. The guest is given them live where it can be, otherwise at its next start from the panel; the node answers neighbour discovery for each where its upstream treats the pool as on-link."),
			get("/api/v1/vms/:id/nics", "Network cards").can("vm:read").cust().out(ok(arr(service.VMNICView{}))).
				note("Read from the node and kept the first time for a VM created before cards were recorded."),
			post("/api/v1/vms/:id/nics", "Add a network card").can("vm:update").cust().in(handler.AddNICRequest{}).
				code(http.StatusCreated).out(obj("success", tTrue, "message", tStr, "data", service.VMNICView{})).
				note("On another bridge than the VM's other cards, with one address from that pool. Staff give `pool_id` (and may choose `mac_address`); customers give `vpc_id`, one of their own private networks on the VM's node. Added live where the VM's machine allows, otherwise at its next start; `message` says which."),
			del("/api/v1/vms/:id/nics/:nic_id", "Remove a network card").can("vm:update").cust().out(obj("success", tTrue, "message", tStr)).
				note("Not the first card. Its addresses go back to their pool. Customers remove only cards on their own private networks."),
			put("/api/v1/vms/:id/nics/:nic_id/mac", "Change a card's MAC address").can("vm:update").in(handler.SetNICMACRequest{}).out(ok(models.VMNIC{})).
				note("Staff only, with the VM stopped. Unicast and unique; the guest's network settings are rewritten for it."),
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
			get("/api/v1/vms/:id/rdns", "Reverse DNS names inside the VM's IPv6 prefix").can("vm:read").cust().
				out(ok(obj("prefix", tStr, "prefix_ptrs", arr(models.VMPrefixPTR{})))).
				note("The VM's own addresses carry their reverse DNS on its address list; this lists the names set for addresses inside the IPv6 prefix routed to it."),
			put("/api/v1/vms/:id/rdns", "Set reverse DNS for one of the VM's addresses").can("vm:update").cust().
				in(obj("ip_address", tStr, "rdns", tStr)).out(ok(obj("ip_address", tStr, "rdns", tStr))).
				note("`ip_address` is an address the VM holds or any address inside the IPv6 prefix routed to it (at most 64 names per prefix). An empty `rdns` clears the name. For customers the hostname must already resolve to the address."),
			get("/api/v1/vms/:id/nat", "NAT VM: public ports and hostnames").can("vm:read").cust().out(ok(service.VMNATInfo{})),
			post("/api/v1/vms/:id/hostnames", "Route a website hostname to a NAT VM").can("vm:update").cust().
				in(service.AddHostnameRequest{}).code(http.StatusCreated).out(ok(models.VMHostname{})),
			post("/api/v1/vms/:id/hostnames/:hostname_id/verify", "Check a hostname's TXT record or address").can("vm:update").cust().
				out(ok(models.VMHostname{})),
			del("/api/v1/vms/:id/hostnames/:hostname_id", "Stop routing a hostname").can("vm:update").cust().out(okMsg),
			get("/api/v1/vms/:id/bandwidth", "This month's transfer").can("vm:read").cust().q("node_id").out(handler.BandwidthUsageResponse{}).
				note("Not wrapped in an envelope. The period starts on the plan's (or the account's) reset day; `total_bytes` holds only the direction the plan counts (`counts`: both, out or in). With pooled transfer, the allowance is measured against the account's pool (`pool_used_gb`, `pool_quota_gb`)."),
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
			get("/api/v1/vms/:id/backups/:backup_id/download", "Download a backup's disk image").can("backup:download").cust().q("disk").raw("application/octet-stream").
				note("302 to a short-lived link at the store for a plain backup in S3; otherwise the image streamed through the panel, decrypted when it was stored encrypted. 409 not_downloadable says why when it cannot be (an incremental backup, or one the panel cannot reach)."),
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
			get("/api/v1/vms/:id/snapshot-schedule", "The VM's snapshot schedule").can("vm:read").cust().out(ok(models.SnapshotSchedule{})).
				note("`data` is null for a VM without one."),
			put("/api/v1/vms/:id/snapshot-schedule", "Set the VM's snapshot schedule").can("vm:read snapshot:create").cust().
				in(service.SnapshotScheduleRequest{}).out(ok(models.SnapshotSchedule{})).
				note("A snapshot every `interval_hours` (1-720), keeping the newest `keep` (1-10) of those it took; the oldest go first. Snapshots taken by hand are never removed, and count against the plan's snapshot allowance, which `keep` must fit in."),
			del("/api/v1/vms/:id/snapshot-schedule", "Remove the VM's snapshot schedule").can("vm:read snapshot:delete").cust().out(msg).
				note("The snapshots it took are kept."),
			get("/api/v1/images", "List images").can("vm:read").cust().q("account_id").out(bare(arr(models.Image{}))).
				note("Disk captures that outlive their VM and can seed a new one."),
			post("/api/v1/images", "Capture a VM's disk as an image").can("snapshot:create").cust().in(handler.CreateImageRequest{}).
				code(http.StatusAccepted).out(bare(models.Image{})),
			post("/api/v1/images/:id/retry", "Retry a failed capture").can("snapshot:create").cust().q("account_id").out(msgData(models.Image{})),
			del("/api/v1/images/:id", "Delete an image").can("snapshot:delete").cust().q("account_id").out(msg),
		),
		group("Networking",
			get("/api/v1/floating-ips", "List floating IPs").can("floating_ip:read").cust().q("account_id").out(okList(models.IPAddress{})),
			post("/api/v1/floating-ips", "Allocate a floating IP for an account").can("network:update").
				in(obj("pool_id", tStr, "user_id", tStr, "region", tStr)).code(http.StatusCreated).out(ok(models.IPAddress{})),
			post("/api/v1/floating-ips/order", "Order a floating IP").can("floating_ip:create").cust().q("account_id").in(obj("region", tStr)).
				code(http.StatusCreated).out(ok(models.IPAddress{})).
				note("`floating_ip:create` is not in the custom role catalog: staff need the Administrator role."),
			post("/api/v1/floating-ips/:id/attach", "Point a floating IP at a VM").can("floating_ip:update").cust().q("account_id").in(obj("vm_id", tStr)).out(ok(models.IPAddress{})),
			post("/api/v1/floating-ips/:id/detach", "Detach a floating IP").can("floating_ip:update").cust().q("account_id").out(ok(models.IPAddress{})),
			del("/api/v1/floating-ips/:id", "Release a floating IP").can("floating_ip:delete").cust().q("account_id").code(http.StatusNoContent),
			get("/api/v1/floating-ips/billing", "Floating IP counts for billing").can("floating_ip:read").cust().q("user_id", "account_id").
				out(ok(obj("user_id", tStr, "total", tInt, "free", tInt, "billable", tInt))),
			get("/api/v1/vpcs", "List VPCs").can("vpc:read").cust().q("account_id").out(okList(models.ManagedNetwork{})),
			post("/api/v1/vpcs", "Create a VPC").can("vpc:create").cust().q("account_id").in(service.CreateVPCRequest{}).code(http.StatusCreated).out(ok(models.ManagedNetwork{})),
			get("/api/v1/vpcs/:id", "Get a VPC").can("vpc:read").cust().q("account_id").out(ok(models.ManagedNetwork{})),
			put("/api/v1/vpcs/:id", "Change a VPC's nameservers").can("vpc:update").cust().q("account_id").in(obj("nameservers", tStr)).out(ok(models.ManagedNetwork{})).
				note("Comma separated, up to four; empty for the built-in ones. Given to VMs set up from now on (new, reinstalled, readdressed)."),
			del("/api/v1/vpcs/:id", "Delete a VPC").can("vpc:delete").cust().q("account_id").code(http.StatusNoContent),
			get("/api/v1/firewall-groups", "List firewall groups").can("firewall_group:read").cust().q("scope", "account_id").out(okList(service.FirewallGroupSummary{})).
				note("Customers see their own groups and the groups staff made for everyone."),
			post("/api/v1/firewall-groups", "Create a firewall group").can("firewall_group:update").cust().q("account_id").in(obj("name", tStr, "description", tStr, "position", tStr)).
				code(http.StatusCreated).out(ok(models.FirewallGroup{})),
			get("/api/v1/firewall-groups/:id", "Get a firewall group").can("firewall_group:read").cust().q("account_id").out(ok(service.FirewallGroupDetail{})),
			put("/api/v1/firewall-groups/:id", "Change a firewall group").can("firewall_group:update").cust().q("account_id").in(obj("name", tStr, "description", tStr, "position", tStr)).
				out(ok(models.FirewallGroup{})),
			del("/api/v1/firewall-groups/:id", "Delete a firewall group").can("firewall_group:update").cust().q("account_id").out(okMsg),
			post("/api/v1/firewall-groups/:id/rules", "Add a rule to a group").can("firewall_group:update").cust().q("account_id").in(handler.AddFirewallRuleRequest{}).
				code(http.StatusCreated).out(ok(models.FirewallGroupRule{})),
			del("/api/v1/firewall-groups/:id/rules/:rule_id", "Remove a rule from a group").can("firewall_group:update").cust().q("account_id").out(okMsg),
			get("/api/v1/regions", "List regions").can("signed-in").q("available").
				out(okList(obj("id", tStr, "slug", tStr, "name", tStr, "country", tStr, "enabled", tBool, "node_count", tInt))),
			post("/api/v1/regions", "Create a region").can("node:update").in(service.CreateRegionRequest{}).code(http.StatusCreated).out(ok(models.Region{})),
			put("/api/v1/regions/:id", "Change a region").can("node:update").in(service.CreateRegionRequest{}).out(ok(models.Region{})),
			del("/api/v1/regions/:id", "Delete a region").can("node:update").code(http.StatusNoContent),
			post("/api/v1/regions/:id/nodes/:node_id", "Put a node in a region").can("node:update").out(okOnly),
			get("/api/v1/ip-pools", "List IP pools").can("network:read").q("limit", "offset", "page", "page_size").out(okListMeta(models.IPPool{})),
			post("/api/v1/ip-pools", "Create an IP pool").can("network:update").in(service.CreateIPPoolRequest{}).code(http.StatusCreated).out(ok(models.IPPool{})),
			get("/api/v1/ip-pools/:id", "Get an IP pool").can("network:read").out(ok(models.IPPool{})),
			put("/api/v1/ip-pools/:id", "Change an IP pool").can("network:update").in(service.UpdateIPPoolRequest{}).out(ok(models.IPPool{})),
			del("/api/v1/ip-pools/:id", "Delete an IP pool").can("network:update").code(http.StatusNoContent),
			get("/api/v1/ip-pools/:id/addresses", "Addresses in a pool").can("network:read").q("limit", "offset", "page", "page_size").out(okListMeta(models.IPAddress{})),
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
			get("/api/v1/abuse/guests", "Guests by connection and packet rate").can("network:read").q("all").
				out(obj("success", tTrue, "data", arr(models.GuestConnection{}), "threshold", tInt, "policy", service.AbusePolicy{})),
			get("/api/v1/abuse/events", "Abuse timeline").can("network:read").q("open", "limit", "offset").out(okListMeta(models.AbuseEvent{})).
				note("Episodes newest first: a guest over a threshold from the first reading over it to the first back under, with up to 30 readings as evidence and what was done. `open=true` keeps those still going on or with an action in force."),
			post("/api/v1/abuse/events/:id/release", "Lift an episode's quarantine, rate limit or null-route").can("network:update").out(ok(models.AbuseEvent{})).
				note("The episode is not acted on again; a new one starts once the guest has been quiet."),
			post("/api/v1/abuse/null-routes", "Null-route an address for a while").can("network:update").in(service.NullRouteInput{}).
				code(http.StatusCreated).out(ok(models.AbuseEvent{})).
				note("Drops all traffic to the address on the node for `minutes` (at most a week); the node lifts it itself when the time is up. Answers 502 with the episode when the node refused, for example an agent too old to null-route."),
			get("/api/v1/abuse/policy", "Abuse policy").can("network:read").out(ok(service.AbusePolicy{})),
			put("/api/v1/abuse/policy", "Save the abuse policy").can("network:update").in(service.AbusePolicy{}).out(ok(service.AbusePolicy{})).
				note("Thresholds are per second. Outbound readings may `alert`, `rate_limit` or `quarantine`; inbound ones `alert` or `null_route`. An action is taken once a guest has been over for `sustain_samples` readings in a row (one a minute). Every action is `alert` until changed."),
			post("/api/v1/abuse/guests/quarantine", "Cut or restore a guest's traffic").can("network:update").
				in(obj("node_id", tStr, "mac", tStr, "quarantined", tBool, "reason", tStr)).out(okOnly),
			get("/api/v1/ip-reputation", "Blocklist status of the address space").can("network:read").q("all").out(okList(models.IPReputation{})),
			post("/api/v1/ip-reputation/check", "Check the address space now").can("network:update").out(msgData(obj("checked", tInt))),
		),
		group("Nodes and storage",
			get("/api/v1/nodes", "List nodes").can("node:read").q("status", "limit", "offset").out(obj("success", tTrue, "message", tStr, "data", arr(handler.NodeListItem{}), "meta", pageMeta)),
			get("/api/v1/node-groups", "Hypervisor groups in use").can("node:read").out(okList(service.NodeGroupSummary{})).
				note("A node's group is set with `PUT /api/v1/nodes/{id}` (`node_group`); a plan's with the plan (`node_group`). New VMs on a plan with a group are placed only on that group's nodes."),
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
			get("/api/v1/nodes/:id/vlan-bridges", "Bridges that carry per-VM VLANs").can("node:read").out(ok(service.NodeVLANBridges{})),
			put("/api/v1/nodes/:id/vlan-bridges", "Enable VLANs on a node's bridges").can("node:update").in(service.NodeVLANBridges{}).out(ok(service.NodeVLANBridges{})).
				note("Each bridge with the uplink port that carries its VLANs tagged. `use_environment` goes back to the agent's MABURVM_VLAN_BRIDGES. Saved once the node has it; each bridge comes back with what the node sees there. Nothing changes on the node's network until a VM on the bridge is given a VLAN."),
			get("/api/v1/nodes/:id/agent-settings", "The agent's environment settings").can("node:read").out(ok(service.NodeAgentSettings{})).
				note("Secrets are reported only as set or not set."),
			get("/api/v1/nodes/:id/nat", "NAT port range of a node").can("node:read").out(ok(service.NodeNATSettings{})),
			put("/api/v1/nodes/:id/nat", "Set a node's NAT port range").can("node:update").in(obj("port_start", tInt, "port_end", tInt)).out(ok(service.NodeNATSettings{})),
			get("/api/v1/nodes/:id/dedicated-cpus", "A node's pool of dedicated cores").can("node:read").out(ok(service.NodeCPUPool{})),
			put("/api/v1/nodes/:id/dedicated-cpus", "Set a node's pool of dedicated cores").can("node:update").in(obj("pool", tStr)).out(ok(service.NodeCPUPool{})).
				note("A host CPU list such as `4-15`. Dedicated-CPU VMs take one core each per vCPU from it. Cores a VM holds must stay in it."),
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
			put("/api/v1/storage/pools/:id/tier", "Set a pool's storage tier").can("storage:update").in(obj("tier", tStr)).out(ok(models.StoragePool{})).
				note("A plan naming a tier (`storage_tier`) puts its VMs' disks only in pools of that tier. Empty clears it."),
			get("/api/v1/storage/tiers", "Storage tiers in use").can("node:read").out(ok(arr(tStr))),
			get("/api/v1/storage/orphans", "Orphaned disks on a node").can("node:read").q("node_id").out(okList(service.OrphanDisk{})).
				note("Disk files and logical volumes in the node's registered pools that no VM on the node uses and no panel record names: leftovers of failed creates, restores, moves and deleted VMs. Files written in the last six hours are left out."),
			post("/api/v1/storage/orphans/delete", "Delete an orphaned disk").can("storage:delete").in(service.OrphanDeleteRequest{}).out(okMsg).
				note("Send the disk as listed, with `confirm` repeating its path. It is listed again here and on the node first, and refused if it is no longer orphaned or has changed (size, time). One MaburVM did not make needs `allow_foreign`."),
		),
		group("Templates and plans",
			get("/api/v1/templates", "List templates").can("signed-in").q("include_nodes").out(handler.SuccessResponse{}).
				note("`data` is a list of templates with, for staff asking `include_nodes`, their state on each node."),
			post("/api/v1/templates", "Add a template").can("template:update").in(handler.CreateTemplateRequest{}).code(http.StatusCreated).out(handler.SuccessResponse{}),
			post("/api/v1/templates/download-url", "Add an ISO from a URL").can("template:update").
				in(obj("url", tStr, "filename", tStr, "checksum_sha256", tStr, "checksum_url", tStr, "skip_checksum", tBool, "os_type", tStr)).out(handler.SuccessResponse{}).
				note("Adds an ISO. A sha256, or a checksum list to read it from, is required unless `skip_checksum` confirms the publisher gives none; plain http needs `allow_http` on the template."),
			get("/api/v1/templates/:id", "Get a template").can("signed-in").q("include_nodes").out(handler.SuccessResponse{}),
			put("/api/v1/templates/:id", "Change a template").can("template:update").in(handler.UpdateTemplateRequest{}).out(handler.SuccessResponse{}),
			del("/api/v1/templates/:id", "Delete a template").can("template:update").out(handler.SuccessResponse{}),
			post("/api/v1/templates/:id/sync", "Copy a template to nodes").can("template:update").in(handler.SyncTemplateRequest{}).out(handler.SuccessResponse{}),
			post("/api/v1/templates/:id/refresh", "Refresh a template on the nodes").can("template:update").in(obj("force", tBool)).
				out(ok(service.TemplateRefreshResult{})).
				note("Reads the template's checksum list again; when the image changed, records the new checksum and has every active node fetch it. VMs built from the old image are copies and stay as they are; each node drops its old copy once nothing uses it. `force` fetches onto the nodes even when nothing changed."),
			post("/api/v1/isos/uploads", "Start uploading an ISO").can("template:update").in(obj("name", tStr, "size", tInt, "sha256", tStr, "os_type", tStr)).
				code(http.StatusCreated).out(ok(obj("upload_id", tStr, "chunk_bytes", tInt))),
			put("/api/v1/isos/uploads/:upload", "Send a chunk of an ISO").can("template:update").q("offset").raw("application/octet-stream").
				out(ok(obj("offset", tInt))).
				note("The body is the chunk's bytes, at most `chunk_bytes`, sent in order: `offset` must be where the upload has got to (409 says where that is)."),
			post("/api/v1/isos/uploads/:upload/finish", "Finish an ISO upload").can("template:update").code(http.StatusCreated).out(ok(models.OSTemplate{})).
				note("Checks the size and, when given, the sha256, adds the ISO, and copies it to every active node."),
			del("/api/v1/isos/uploads/:upload", "Cancel an ISO upload").can("template:update").out(okOnly),
			get("/api/v1/isos/drivers", "The drivers ISO").can("template:update").out(ok(service.ImageSettings{})),
			put("/api/v1/isos/drivers", "Choose the drivers ISO").can("template:update").in(service.ImageSettings{}).out(ok(service.ImageSettings{})).
				note("The ISO put in a Windows install's second drive (the virtio drivers disc) when an order asks for it. Empty clears it."),
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
			get("/api/v1/users/:id/notes", "Staff notes on an account").can("user:read").out(okList(service.StaffNote{})),
			get("/api/v1/users/:id/transfer", "An account's transfer settings").can("user:read").out(ok(service.AccountTransfer{})),
			put("/api/v1/users/:id/transfer", "Set an account's reset day and pooled transfer").can("user:update").in(service.AccountTransfer{}).
				out(ok(service.AccountTransfer{})).
				note("`reset_day` (1-28) overrides the plans' reset day for the account's VMs; null follows the plans. `pooled` makes the account's VMs share the sum of their allowances; VMs without one stay outside the pool."),
			post("/api/v1/users/:id/notes", "Add a staff note to an account").can("user:read user:update").in(obj("body", tStr)).
				code(http.StatusCreated).out(ok(service.StaffNote{})),
			put("/api/v1/users/:id/notes/:note_id", "Change a staff note on an account").can("user:read user:update").in(obj("body", tStr)).out(ok(service.StaffNote{})),
			del("/api/v1/users/:id/notes/:note_id", "Delete a staff note on an account").can("user:read user:update").out(ok(nil)),
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
			get("/api/v1/dashboard/vms", "VM table for the dashboard").can("vm:read").q("search", "status", "node", "tag", "sort", "page", "page_size").
				out(okPaged(obj("vms", arr(tAny), "total", tInt, "page", tInt, "page_size", tInt, "all", tInt, "by_status", tAny))).
				note("Staff only: customers get 403."),
			get("/api/v1/dashboard/mine", "A customer's VMs at a glance").can("vm:read").cust().out(ok(obj("vms", arr(tAny)))),
			get("/api/v1/events/vm-status", "VM status stream").can("vm:read").cust().raw("text/event-stream").
				note("Server-sent events: a snapshot of every visible VM's status, then each change."),
			get("/api/v1/audit-logs", "Audit log").can("audit:read").q("page", "page_size", "action", "user_id", "resource_type", "start_date", "end_date").
				out(pageNum(models.AuditLog{})),
			get("/api/v1/alerts", "Alerts").can("ops:read").q("page", "page_size", "state").pageInList("alerts", models.Alert{}),
			post("/api/v1/alerts/:id/close", "Close an alert").can("ops:update").out(ok(obj("closed", tBool))),
			get("/api/v1/queue", "Job queue").can("ops:read").q("state", "kind", "node_id", "vm_id", "q", "page", "page_size").
				out(okPaged(obj("counts", arr(obj("state", tStr, "count", tInt)), "jobs", arr(handler.QueueJob{}), "workers", js{"type": "object", "additionalProperties": tInt},
					"saved_workers", js{"type": "object", "additionalProperties": tInt}, "total", tInt, "page", tInt, "page_size", tInt,
					"kinds", arr(tStr), "held", tBool))).
				note("Newest first, 50 a page (at most 200). `q` matches the job number, its kind, the VM or node it is about, its operation and its last error. `held` is true while maintenance mode holds the queue."),
			get("/api/v1/queue/jobs/:id", "A job with its arguments and every failed attempt").can("ops:read").out(ok(handler.JobDetail{})).
				note("Credentials in the arguments come back as `[REDACTED]`."),
			put("/api/v1/queue/workers", "Set workers per queue").can("settings:update").
				in(js{"type": "object", "additionalProperties": tInt, "description": "Workers by queue: critical, default, batch, audit. Applies after a panel restart."}).out(ok(tAny)),
			post("/api/v1/queue/jobs/:id/retry", "Retry a job").can("ops:update").out(ok(tAny)),
			post("/api/v1/queue/jobs/:id/cancel", "Cancel a job").can("ops:update").out(ok(tAny)),
			get("/api/v1/security/checks", "Security centre checks").can("settings:read").out(okList(handler.SecurityCheck{})),
			get("/api/v1/setup", "First steps checklist").can("staff").out(ok(tAny)),
			put("/api/v1/setup/dismissed", "Hide the first steps checklist").can("settings:update").in(obj("dismissed", tBool)).out(ok(tAny)),
			get("/api/v1/export/:kind", "Export as CSV").can("admin:access").raw("text/csv").
				note("`kind` is `vms`, `users`, `ip-addresses` or `audit-log`."),
			get("/api/v1/logs/requests", "Recent API requests").can("audit:read").q("method", "status", "q", "min_ms", "page", "page_size").
				out(okPaged(obj("requests", arr(middleware.RequestLogEntry{}), "total", tInt, "page", tInt, "page_size", tInt, "kept", tInt))).
				note("The last 10,000 requests to the API and the billing webhook, newest first, kept in the panel's memory: a restart empties it. `status` is a class (`4xx`) or a code; `q` matches the path, account, API key, address or request id. Query strings are never recorded."),
			get("/api/v1/logs/ip-allocations", "Address assignments and releases").can("network:read").q("q", "state", "kind", "page", "page_size").
				out(okPaged(obj("events", arr(handler.IPAllocationEvent{}), "total", tInt, "page", tInt, "page_size", tInt))).
				note("Every holding of an address, newest change first. `q` matches the address, VM name, owner's email or VM id; `state` is `held` or `released`."),
			get("/api/v1/reports/capacity", "Capacity and allocation by day").can("report:read").q("scope", "days", "format").
				out(ok(obj("scope", tStr, "days", tInt, "series", arr(service.CapacitySeries{})))).
				note("`scope` is `node` (default), `pool` or `region`; `days` up to 730 (default 90). One point a day: what was allocated against what there was, and the day's average use. `format=csv` answers a spreadsheet."),
			get("/api/v1/reports/usage", "What each customer uses").can("report:read").q("month", "format").
				out(ok(obj("month", tStr, "customers", arr(service.CustomerUsage{})))).
				note("Customer accounts with VMs or transfer in `month` (YYYY-MM, default this month): VMs, vCPU, memory and disk held now, and the month's transfer. `format=csv` answers a spreadsheet."),
			get("/api/v1/announcements/current", "Announcements for this account").can("signed-in").out(okList(service.CustomerAnnouncement{})).
				note("Customers get the announcements showing now that are meant for them; staff get none."),
			get("/api/v1/announcements", "Announcements").can("announcement:read").out(okList(service.Announcement{})),
			post("/api/v1/announcements", "Write an announcement").can("announcement:update").in(service.AnnouncementInput{}).
				code(http.StatusCreated).out(ok(service.Announcement{})).
				note("`audience.type` is `all`, `nodes`, `regions`, `plans` or `vms`, with the chosen ids; the customers who own a VM there see it. `kind` is `service` or `marketing`. `{{name}}`, `{{greeting}}` and `{{brand}}` in the message are filled in each email."),
			put("/api/v1/announcements/:id", "Change an announcement").can("announcement:update").in(service.AnnouncementInput{}).out(ok(service.Announcement{})),
			del("/api/v1/announcements/:id", "Take an announcement down").can("announcement:update").out(ok(nil)),
			post("/api/v1/announcements/preview", "Who an announcement reaches, and its email").can("announcement:update").in(service.AnnouncementInput{}).
				out(ok(service.AnnouncementPreview{})),
			post("/api/v1/announcements/:id/email", "Email an announcement").can("announcement:update").in(obj("per_minute", tInt)).
				out(ok(obj("queued", tInt, "per_minute", tInt))).
				note("Queues one email per customer in the audience who has not had it, spread at `per_minute` (default 60, at most 600), through the email log. A marketing one skips customers who unsubscribed and carries an unsubscribe link. Safe to send again: nobody gets it twice."),
			post("/api/v1/unsubscribe", "Stop marketing email").can("public").in(obj("token", tStr)).out(ok(obj("unsubscribed", tBool))).
				note("The token comes from the link in a marketing email. Service notices still reach the account."),
			get("/api/v1/maintenance", "Maintenance mode").can("signed-in").out(ok(handler.MaintenanceView{})).
				note("While on, customers and the billing system cannot change anything (503 with `code: maintenance`), staff carry on. Staff also see who switched it on and the jobs still running."),
			put("/api/v1/maintenance", "Switch maintenance mode").can("owner").in(obj("enabled", tBool, "message", tStr, "hold_jobs", tBool)).
				out(ok(handler.MaintenanceView{})).
				note("`hold_jobs` starts no new job until maintenance ends; running ones finish. `panel maintenance on|off` does the same from the host (scripts/upgrade-panel.sh)."),
			get("/api/v1/system/update", "The update notice").can("settings:read").out(ok(obj("status", service.UpdateStatus{}, "can_manage", tBool))),
			put("/api/v1/system/update", "Turn the update notice on or off").can("owner").in(service.UpdateCheckSettings{}).
				out(ok(obj("status", service.UpdateStatus{}, "can_manage", tBool))).
				note("Off by default. When on, the panel reads the GitHub release feed of `repository` once a day; the request carries nothing about this panel."),
			post("/api/v1/system/update/check", "Check for a new release now").can("owner").out(ok(obj("status", service.UpdateStatus{}, "can_manage", tBool))),
			post("/api/v1/system/update/dismiss", "Hide the notice for a release").can("owner").in(obj("tag", tStr)).
				out(ok(obj("status", service.UpdateStatus{}, "can_manage", tBool))),
			get("/api/v1/billing/deliveries", "Events sent to the billing system").can("billing:read").q("page", "page_size", "status").
				pageInList("deliveries", service.WebhookDelivery{}),
			post("/api/v1/billing/deliveries/:id/retry", "Send an event again").can("billing:update").out(ok(obj("queued", tBool))),
			get("/api/v1/webhooks", "Outbound webhooks").can("webhook:read").
				out(obj("success", tTrue, "data", arr(service.WebhookEndpoint{}), "events", arr(service.WebhookEventGroup{}))).
				note("Each webhook's address, events and whether a secret is saved (the secret is never shown), and in `events` every event one can subscribe to."),
			post("/api/v1/webhooks", "Add an outbound webhook").can("webhook:update").in(service.WebhookEndpointRequest{}).
				code(http.StatusCreated).out(ok(service.WebhookEndpoint{})).
				note("`events` names events from the catalog, or `*` for every event including ones added later. The secret, at least 32 characters, signs each delivery: `X-Webhook-Signature` is the hex HMAC-SHA256 of the raw body."),
			put("/api/v1/webhooks/:id", "Change an outbound webhook").can("webhook:update").in(service.WebhookEndpointRequest{}).
				out(ok(service.WebhookEndpoint{})).note("Leave `secret` out to keep the saved one; send an empty one to remove it. `enabled: false` stops new deliveries and retries."),
			del("/api/v1/webhooks/:id", "Remove an outbound webhook").can("webhook:update").out(okMsg).note("Its delivery log goes with it."),
			post("/api/v1/webhooks/:id/test", "Send a test event now").can("webhook:update").out(ok(service.WebhookDelivery{})).
				note("Sends `webhook.test` once, waits for the answer (ten seconds at most) and returns the delivery: `status` delivered or failed, with the receiver's answer. Works on a switched-off webhook too."),
			get("/api/v1/webhooks/:id/deliveries", "What was sent to a webhook").can("webhook:read").q("page", "page_size", "status").
				pageInList("deliveries", service.WebhookDelivery{}),
			post("/api/v1/webhooks/:id/deliveries/:delivery_id/retry", "Send a webhook event again").can("webhook:update").out(ok(obj("queued", tBool))),
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
				note("JSON-RPC 2.0 over Streamable HTTP for AI agents. Tools cover VMs (power, create, change, reinstall, delete, passwords), snapshots, backups, firewall rules, addresses and floating IPs, templates and plans, and for staff nodes, IP pools, accounts, alerts and migrations. `tools/list` shows only the tools the key may use: those its scope reaches and whose route permissions its owner holds (a read-only key sees only reading tools). Each call is checked again like the REST route it maps to."),
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
			get("/api/v1/settings/system/rescue", "The rescue images in force").can("settings:read").
				out(ok(obj("iso_url", tStr, "source", tStr, "image_url", tStr, "image_sha256", tStr))).
				note("`iso_url` is the rescue ISO (saved, or RESCUE_ISO_URL); `image_url` the built-in rescue system's disk image. Both are saved through `PUT /api/v1/settings/system` as section `rescue` (`isoUrl`, `imageUrl`, `imageSha256`)."),
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
			get("/api/v1/settings/system/emails", "Email log").can("settings:read").q("page", "page_size", "status", "template", "q").pageInList("emails", service.EmailLogEntry{}),
			post("/api/v1/settings/system/emails/:id/retry", "Send an email again").can("settings:update").out(ok(obj("queued", tBool))),
			get("/api/v1/settings/system/dns-provider", "Nameserver connection").can("settings:read").out(ok(service.DNSProviderStatus{})),
			put("/api/v1/settings/system/dns-provider", "Save the nameserver connection").can("settings:update").
				in(obj("kind", tStr, "url", tStr, "server_id", tStr, "api_key", tStr)).out(ok(service.DNSProviderStatus{})).
				note("`kind` is `powerdns` (the default; `url` and `server_id` name the server) or `cloudflare` (an API token with Zone DNS edit rights in `api_key`; zones must already be on the account)."),
			del("/api/v1/settings/system/dns-provider", "Remove the nameserver connection").can("settings:update").out(ok(service.DNSProviderStatus{})),
			post("/api/v1/settings/system/dns-provider/test", "Test a nameserver connection").can("settings:update").
				in(obj("kind", tStr, "url", tStr, "server_id", tStr, "api_key", tStr)).out(okMsg),
			get("/api/v1/settings/system/backup-destinations", "Backup destinations").can("settings:read").out(okList(service.BackupDestinationView{})),
			post("/api/v1/settings/system/backup-destinations", "Add a backup destination").can("settings:update").in(service.BackupDestinationInput{}).
				code(http.StatusCreated).out(ok(service.BackupDestinationView{})),
			put("/api/v1/settings/system/backup-destinations/:id", "Change a backup destination").can("settings:update").
				in(service.BackupDestinationInput{}).out(ok(service.BackupDestinationView{})),
			post("/api/v1/settings/system/backup-destinations/:id/default", "Send new backups to this destination").can("settings:update").out(okMsg),
			del("/api/v1/settings/system/backup-destinations/:id", "Remove a backup destination").can("settings:update").out(okMsg),
			post("/api/v1/settings/system/backup-destinations/:id/encryption", "Encrypt new backups, or stop").can("settings:update").
				in(obj("encrypt", tBool)).out(ok(service.BackupDestinationView{})).
				note("Backups are encrypted on the node before they are uploaded. Turning it off keeps every key, so encrypted backups still restore."),
			post("/api/v1/settings/system/backup-destinations/:id/rotate-key", "Encrypt new backups with a new key").can("settings:update").
				out(ok(service.BackupDestinationView{})).note("Older keys are kept for the backups made with them."),
			post("/api/v1/settings/system/backup-destinations/:id/export-keys", "Reveal a destination's encryption keys").can("admin:access").
				out(okList(service.BackupKeyExport{})).note("For safekeeping outside the panel: with them, cmd/backup-decrypt restores a backup file without the panel."),
			post("/api/v1/settings/system/backup-destinations/test", "Test a destination from the panel and nodes").can("settings:update").
				in(obj("id", tStr, "node_ids", arr(tStr))).out(ok(obj("results", arr(service.BackupDestinationTestResult{})))),
			post("/api/v1/settings/system/backup-destinations/scan-host-key", "Read an SFTP server's host key").can("settings:update").
				in(obj("host", tStr, "port", tInt)).out(ok(obj("host_key", tStr, "fingerprint", tStr))),
		),
		group("Teams and customer area",
			get("/api/v1/portal", "What the customer area offers").can("signed-in").out(ok(service.CustomerPortal{})).
				note("Support, billing and status links, the ISOs customers may mount, whether they may mount their own, how address requests are handled (`off`, `approval`, `auto`) and how many days of graphs are kept."),
			get("/api/v1/account/members", "An account's members and invitations").can("signed-in").q("account_id").out(okList(service.AccountMember{})).
				note("The caller's own account, or with `account_id` one they are an admin of. Members with empty `vm_ids` and `project_ids` reach every VM of the account."),
			post("/api/v1/account/members", "Invite someone to the account").can("signed-in").in(obj("account_id", tStr, "email", tStr, "role", tStr, "vm_ids", arr(tStr), "project_ids", arr(tStr))).
				code(http.StatusCreated).out(obj("success", tTrue, "data", service.AccountMember{}, "warning", tStr)).
				note("`role` is `admin` (everything the owner does, the team too), `technical` (runs the VMs), `billing` (usage and billing, VMs view-only) or `readonly`. The invitation is emailed and works for 7 days; `warning` says when it was saved but not sent."),
			put("/api/v1/account/members/:id", "Change a member's role or reach").can("signed-in").in(obj("account_id", tStr, "role", tStr, "vm_ids", arr(tStr), "project_ids", arr(tStr))).
				out(ok(service.AccountMember{})),
			del("/api/v1/account/members/:id", "Remove a member or revoke an invitation").can("signed-in").q("account_id").out(okOnly),
			get("/api/v1/account/memberships", "The accounts the caller works in").can("signed-in").
				out(ok(obj("memberships", arr(service.Membership{}), "member_only", tBool))).
				note("`member_only`: the caller's own account was made by accepting an invitation and cannot order anything of its own until staff allow it."),
			del("/api/v1/account/memberships/:id", "Leave an account").can("signed-in").out(okOnly),
			post("/api/v1/account/invitations/lookup", "An invitation, before accepting it").can("public").limit().in(obj("token", tStr)).out(ok(service.InvitationView{})).
				note("The token from the email link, in the body so it stays out of request logs. 410 `invitation_invalid` when it was revoked, accepted or has expired."),
			post("/api/v1/account/invitations/accept", "Accept an invitation").can("public").limit().in(service.AcceptInput{}).out(ok(obj("email", tStr))).
				note("An existing account with the invited address is linked as it is; otherwise one is made with `name` and `password`, and it can sign in at once. It cannot order servers of its own until staff allow it."),
			get("/api/v1/account/projects", "An account's projects").can("signed-in").q("account_id").out(okList(service.Project{})),
			post("/api/v1/account/projects", "Add a project").can("signed-in").in(obj("account_id", tStr, "name", tStr, "description", tStr)).code(http.StatusCreated).out(ok(service.Project{})),
			put("/api/v1/account/projects/:id", "Rename a project").can("signed-in").in(obj("account_id", tStr, "name", tStr, "description", tStr)).out(ok(service.Project{})),
			del("/api/v1/account/projects/:id", "Delete a project").can("signed-in").q("account_id").out(okOnly).
				note("Its VMs stay, in no project. A member limited to it alone loses their membership."),
			put("/api/v1/vms/:id/project", "Move a VM into a project").can("vm:update").cust().in(obj("project_id", tStr)).out(ok(obj("project_id", tStr))).
				note("`\"\"` takes it out of its project."),
			get("/api/v1/vms/:id/ip-requests", "A VM's address requests").can("vm:read").cust().out(okList(service.IPRequest{})),
			post("/api/v1/vms/:id/ip-requests", "Ask for another address").can("vm:update").cust().in(obj("family", tStr, "reason", tStr)).code(http.StatusCreated).
				out(obj("success", tTrue, "data", service.IPRequest{}, "warning", tStr)).
				note("`family` is `ipv4` or `ipv6`. Refused at once when the VM's plan allows no more. With automatic grants the address is assigned now (`status: granted`); otherwise it waits for staff (`pending`)."),
			del("/api/v1/vms/:id/ip-requests/:request_id", "Withdraw a pending request").can("vm:update").cust().out(okOnly),
			get("/api/v1/ip-requests", "Address requests waiting for staff").can("network:read").q("status", "limit", "offset", "page", "page_size").
				out(obj("success", tTrue, "meta", pageMeta, "data", arr(service.IPRequest{}), "total", tInt, "limit", tInt, "offset", tInt)),
			post("/api/v1/ip-requests/:id/approve", "Grant an address request").can("network:update").out(obj("success", tTrue, "data", service.IPRequest{}, "warning", tStr)),
			post("/api/v1/ip-requests/:id/reject", "Decline an address request").can("network:update").in(obj("note", tStr)).out(ok(service.IPRequest{})),
			get("/api/v1/users/:id/team", "A customer's team, projects and memberships").can("user:read").
				out(ok(obj("members", arr(service.AccountMember{}), "projects", arr(service.Project{}), "memberships", arr(service.Membership{}), "member_only", tBool))),
			put("/api/v1/users/:id/member-only", "Let an invited account order on its own").can("user:update").in(obj("member_only", tBool)).out(ok(obj("member_only", tBool))),
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
	"Health":                  "Probes for orchestrators and monitoring.",
	"Authentication":          "Signing in and out, single sign-on and the first administrator.",
	"Branding":                "What the sign-in page shows before anyone signs in.",
	"Account":                 "What any signed-in account does with itself: profile, two-factor, sessions, keys, scripts, notifications.",
	"VMs":                     "Virtual machines: lifecycle, power, console, credentials, limits and moves.",
	"VM networking":           "A VM's addresses, interfaces, port forwards, firewall, reverse DNS, NAT hostnames and transfer.",
	"Backups and snapshots":   "Backups, schedules, snapshots and images.",
	"Networking":              "Floating IPs, VPCs, firewall groups, regions, IP pools, DNS, abuse and reputation.",
	"Nodes and storage":       "Hypervisor nodes, their installer and agent, drains and storage pools.",
	"Templates and plans":     "Operating system templates and the plans customers order.",
	"Customers and staff":     "Accounts, staff roles, impersonation and support access.",
	"Operations":              "Dashboard, audit log, alerts, job queue, exports, billing deliveries, outbound webhooks, imports and MCP.",
	"Settings":                "System settings: panel, branding, quotas, alerts, email, DNS and backup destinations.",
	"Billing webhook":         "The signed webhook a billing system drives the panel through, and the node installer.",
	"Teams and customer area": "Members, invitations and projects of customer accounts; address requests; what the customer area offers.",
	"Webhooks":                "Requests the panel sends to you.",
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
	"outboundEvent": {
		summary: "Event for an operator's webhook",
		desc: "Sent to each webhook added under System settings, API, Outbound webhooks, for the events it subscribed to: VM lifecycle (the billing events' VM events), " +
			"`backup.completed` and `backup.failed`, `alert.raised` and `alert.resolved`, `bandwidth.overage` and `floating_ip.usage`, " +
			"`user.created`, `user.suspended`, `user.unsuspended` and `user.deleted`, and `webhook.test` from the Send test button. " +
			"Headers are those of the billing events: `X-Webhook-Event`, `X-Webhook-Id` and, with a secret saved, `X-Webhook-Signature` (hex HMAC-SHA256 of the raw body, no prefix). " +
			"Each delivery is stored and retried up to 10 times over about four hours; the webhook's log shows each one. Fields may be added; none are renamed or removed.",
		body: obj(
			"id", described(tStr, "Delivery id; repeats on a retry."),
			"event", js{"type": "string", "enum": []any{service.BillingEventVMCreated, service.BillingEventVMFailed, service.BillingEventVMSuspended,
				service.BillingEventVMUnsuspended, service.BillingEventVMDestroyed, service.EventBackupCompleted, service.EventBackupFailed,
				service.EventAlertRaised, service.EventAlertResolved, service.BillingEventOverage, service.BillingEventFloatingIP,
				service.EventUserCreated, service.EventUserSuspended, service.EventUserUnsuspended, service.EventUserDeleted, service.EventWebhookTest}},
			"sent_at", tTime,
			"vm_id", described(tStr, "VM and backup events."), "user_id", tStr, "user_ref", tStr, "hostname", tStr, "status", tStr,
			"external_ref", tStr, "ip", tStr, "ips", arr(tStr),
			"backup_id", described(tStr, "Backup events."), "backup_type", tStr, "mode", tStr, "size_bytes", js{"type": "integer", "format": "int64"},
			"reason", described(tStr, "backup.failed and vm.failed: why."),
			"alert_id", described(tStr, "Alert events."), "kind", tStr, "severity", tStr, "title", tStr, "detail", tStr, "state", tStr,
			"resource_type", tStr, "resource_id", tStr, "resource_name", tStr, "fired_at", tTime, "resolved_at", tTime,
			"email", described(tStr, "Account events."), "name", tStr, "role", tStr,
			"source", described(tStr, "Account events: staff, billing or signup."),
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
- **API key.** Make one under Settings, API keys and send ` + "`X-API-Key: mvk_...`" + ` or ` + "`Authorization: Bearer mvk_...`" + `. A key acts as its owner, with the owner's staff role as it is at each call, narrowed by its **scope**: ` + "`full`" + `, ` + "`read_only`" + ` (GET only, MCP excepted), ` + "`vm_power`" + ` (list VMs, start, stop, restart) or ` + "`billing`" + ` (billing, plans, accounts and usage). An **own resources only** key makes a staff owner act as a customer. A key may list the addresses it may be used from and may expire. No narrowed key may manage API keys or account settings. Operators can switch API keys off (Settings, System, API).

Two-factor, IP whitelists and suspension apply to every method. While "Require two-factor authentication for administrators" is on (Settings, System, Security), staff without two-factor get ` + "`403 two_factor_required`" + ` until they set it up (API keys excepted).

## Who may call what

Each operation states its access as ` + "`x-access`" + ` and in words:

- ` + "`public`" + `, ` + "`signed-in`" + ` (any account, acting on itself), ` + "`staff`" + ` (any staff account);
- otherwise the staff permissions it needs, all of them, ` + "`a|b`" + ` meaning either. The catalog is ` + "`GET /api/v1/staff-roles`" + `. ` + "`admin:access`" + ` is the Administrator and Owner roles.

` + "`x-customer: true`" + ` means customers may call it too, on their own resources; another tenant's resource answers 404, not 403. Staff viewing as a customer act as that customer and cannot touch the customer's password, email, two-factor, sessions, API keys or initial passwords.

## Answers

Successful answers are JSON objects. New routes answer ` + "`{\"success\": true, \"data\": ...}`" + `; some older ones still answer ` + "`{\"message\": \"...\", \"data\": ...}`" + `, ` + "`{\"data\": ...}`" + ` alone, or the object itself, and each operation documents its own.

**Paginated lists** carry ` + "`meta: {page, per_page, total}`" + ` beside ` + "`data`" + `, and ` + "`success: true`" + `. Ask with ` + "`page`" + ` (from 1) and ` + "`per_page`" + ` (` + "`page_size`" + ` is the same); lists read by ` + "`limit`" + ` and ` + "`offset`" + ` accept those too. The fields lists had before ` + "`meta`" + ` stay for the clients that read them:

| Older fields | Used by |
|---|---|
| ` + "`data`" + ` array, ` + "`total`" + `, ` + "`limit`" + `, ` + "`offset`" + `, ` + "`has_more`" + ` | backups, snapshots; VMs add ` + "`page`" + ` and ` + "`totalPages`" + ` |
| ` + "`data`" + ` array, ` + "`total`" + `, ` + "`page`" + `, ` + "`page_size`" + `, ` + "`totalPages`" + ` | accounts, audit log |
| ` + "`data: {<items>: [...], total, page, page_size}`" + ` | alerts, billing and webhook deliveries, email log, job queue, request and address logs, dashboard VMs |

Lists without ` + "`meta`" + ` return everything at once.

**Errors** all have one shape, whichever route answers:

` + "```json\n{\"success\": false, \"error\": {\"code\": \"forbidden\", \"message\": \"Insufficient permissions\", \"details\": {}}, \"message\": \"Insufficient permissions\"}\n```" + `

` + "`error.code`" + ` is stable: a route's own (` + "`two_factor_required`" + `, ` + "`maintenance`" + `, ` + "`api_key_address`" + `, ` + "`email_exists`" + `...) or one for the status (` + "`bad_request`" + `, ` + "`unauthorized`" + `, ` + "`forbidden`" + `, ` + "`not_found`" + `, ` + "`conflict`" + `, ` + "`rate_limited`" + `, ` + "`unavailable`" + `, ` + "`internal_error`" + `). ` + "`details`" + ` appears when the route has more to say. The top-level ` + "`message`" + ` repeats ` + "`error.message`" + `, and a top-level ` + "`code`" + ` stays on the routes that sent one. Before this envelope ` + "`error`" + ` was a string; read ` + "`error.message`" + ` (or ` + "`message`" + `). The MCP endpoint answers JSON-RPC errors in JSON-RPC's own shape. Writes that start work on a node run as background jobs and answer at once (often 202 or a job id); poll the resource.

## Rate limits

Every signed-in account and every API key gets "API requests per minute" (Settings, System, API; default 1200, 0 turns it off), with ten seconds' worth allowed at once. Past it the panel answers 429 with ` + "`Retry-After`" + `. Sign-in, password reset, single sign-on, first-run and the node installer endpoints are limited per client address (one request every two seconds on average, ten at once). The billing webhook is not counted. Counts are kept in the panel's memory.

## Webhooks

The panel receives signed events from a billing system at ` + "`POST /webhooks/billing`" + ` and sends three kinds of its own, described under webhooks: billing events, events for the webhooks an operator adds (each with its own events, secret and delivery log), and operator alerts.

## Stability

This is version 1. Fields may be added to answers; existing ones are not renamed or removed without a note in the release.`
