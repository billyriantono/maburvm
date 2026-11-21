# User guide

For customers who rent servers on a MaburVM panel. Your provider may show the
panel under their own name and colours, and may have switched some of the
features below off; where something is missing, ask them.

## Contents

1. [Signing in](#1-signing-in)
2. [Your account](#2-your-account)
3. [Ordering a server](#3-ordering-a-server)
4. [Running a server](#4-running-a-server)
5. [Console and passwords](#5-console-and-passwords)
6. [Reinstall, rescue and ISOs](#6-reinstall-rescue-and-isos)
7. [Networking](#7-networking)
8. [Firewall](#8-firewall)
9. [Backups, snapshots and images](#9-backups-snapshots-and-images)
10. [Transfer and speed](#10-transfer-and-speed)
11. [API keys and automation](#11-api-keys-and-automation)
12. [When something goes wrong](#12-when-something-goes-wrong)

## 1. Signing in

- **First time.** If your provider created the account for you, you get an
  email with a link to choose your password. Otherwise use **Forgot password**
  on the sign-in page with the email your provider has.
- **From your billing account.** "Open control panel" in your provider's
  billing area signs you in with one click. The link works once, for one minute.
- **Single sign-on.** If the sign-in page shows a "Sign in with SSO" button,
  your provider lets you sign in through their identity service.
- **Two-step sign-in.** If you turned it on, the panel asks for a code from
  your authenticator app after your password, whichever way you signed in.
- After several wrong passwords or codes the account locks for a while, and the
  page may ask you to complete a check (a captcha) first. Resetting your
  password unlocks it.

## 2. Your account

**Profile** (`/client/settings/profile`):

- Change your name, email (needs your current password) and password.
  Changing your password signs you out everywhere.
- **Two-step sign-in**: scan the code with an authenticator app, enter a code
  to confirm, and keep the backup codes somewhere safe. Each backup code signs
  you in once if you lose the phone. Turning it off needs a fresh code.
- **Sessions**: every place you are signed in, with where and when. Sign one
  out, or "sign out everywhere else".
- **Sign-in history**: every attempt on your account, good or bad.

**SSH Keys**: public keys you can install on new servers. Add one you have, or
let the panel generate a pair (the private key is shown once and not kept).

**Notifications** (the bell): your server was suspended or restored, a backup
failed, your transfer reached a warning level, your password or two-step
sign-in changed, an API key was made. The same events are emailed to you.

## 3. Ordering a server

**Order VM** (if your provider allows ordering here; otherwise order through
their billing area):

1. **Location.** Where the server runs. It cannot move to another location
   later.
2. **Plan.** The size: CPU, memory, disk, speed, monthly transfer, and price if
   your provider shows one.
3. **Operating system**, or one of your images.
4. **Hostname.**
5. **Network** (optional): a public address, or one of your private networks in
   that location.
6. **SSH keys** (optional, Linux): installed for `root`.

The order page then shows the login password **once**: `root` for Linux,
`Administrator` for Windows (Remote Desktop; Windows needs about ten minutes to
finish setting itself up), `admin` for MikroTik RouterOS. Save it. The server
builds in a few minutes; its page shows the progress.

What you may hold in total (servers, vCPUs, memory, disk) is your account's
quota; **Dashboard** shows it.

## 4. Running a server

**My VMs** lists your servers with their state. A server's page opens on a dark
strip with its state, name, power controls and the addresses you copy, then
tabs:

| Tab | What is there |
|---|---|
| Overview | Live CPU, memory, disk and network readings over time, and the addresses and login. |
| Console | The screen of the server in your browser, and an SSH console. |
| Network | Addresses, port forwards, reverse DNS, floating IPs. |
| Snapshots | Snapshots of the disk. |
| Backups | Backups, schedules, restore. |
| Firewall | Inbound rules, default policy, firewall groups. |
| Activity | What was done to the server, by whom, and when. |
| Settings | Name, reinstall, rescue, ISO, extra disks, clone, delete. |

**Power.** Start, Stop (asks the system to shut down), Restart and Power off
(cuts power, like pulling the plug). A server that is **suspended** by your
provider cannot be started until they restore it; usually a billing matter.

**Extra disks** (if allowed): Settings, Add disk. **Detaching a disk deletes
it** and everything on it.

**Clone** (if allowed) makes a copy of the server as a new one.

**Delete** removes the server and its disks. A server your billing account pays
for usually cannot be deleted here: cancel it in the billing area instead, and
it is removed.

"State unknown" means the panel cannot reach the server's host right now. The
server may well be running; your provider has been alerted.

## 5. Console and passwords

- **Console**: the server's screen, even when its network is broken. The
  connection is opened for you with a short-lived token. If the console does
  not connect, **Repair** may restart the server, and says so first.
- **SSH console**: a terminal in your browser, for when you have no SSH client.
- **Initial password**: while it is still kept, Overview can reveal the login
  password the server was created with. **Forget** removes it from the panel
  for good.
- **Reset password** sets a new `root` or `Administrator` password. It needs
  the server running with its guest agent. On RouterOS 7 the router restarts
  to apply it.

## 6. Reinstall, rescue and ISOs

- **Reinstall** (Settings) erases the disk and installs the system you choose,
  of any kind; addresses and the console password stay. A new login password
  is shown once. Windows needs at least 2 GB of memory and a 32 GB disk.
- **Rescue mode** boots a stopped server from a rescue system so you can repair
  its disk from the console. **Leave rescue** boots it normally again. Your
  provider chooses the rescue system; if the button is disabled, rescue is not
  offered.
- **Attach ISO**: boot from an installer by URL. The URL must be public; very
  large files and private addresses are refused. Detach it when done.

## 7. Networking

- **Addresses.** The Network tab lists your server's IPv4 and IPv6 addresses.
  If your provider adds or changes one, the server takes it at once when its
  guest agent can apply it, otherwise at its next start from the panel; a note
  on the page says which.
- **Reverse DNS**: set the name each of your addresses answers to.
- **Private networks** (Private Networks): your own subnets, for servers to
  talk privately. Each lives in one location; servers join one there. You may
  use any private range, even one other customers use.
- **Floating IPs** (Floating IPs): public addresses you order, attach to a
  server in the same location, and move to another server without touching
  either system. They stay yours when a server is deleted, until you release
  them. Your provider may bill them.
- **Port forwards**: publish a port of the host to a port of your server, for a
  server on a private address. A port is shared by everyone on the host, so a
  port someone else holds is refused.
- **NAT servers.** On a NAT plan your server has a private address and a block
  of public ports on its host; its first port goes to SSH (Remote Desktop on
  Windows). You forward more ports inside your block only. **Hostnames** route
  a website name to it: prove the name with a TXT record
  `_maburvm-verify.<hostname>`, or by pointing the name at the host's address,
  then **Verify**. Web traffic is routed by name; HTTPS passes through, so the
  certificate stays on your server.
- **Mail.** Your provider may block outgoing mail on port 25. Ask them if you
  need it.

## 8. Firewall

The Firewall tab holds inbound rules (protocol, port, source) and a **default
policy** for traffic no rule matches: accept or drop. New servers allow SSH (or
Remote Desktop), ping and web traffic. **Firewall groups** (Firewall groups in
the menu) are rule sets you attach to many servers; a group sits before a
server's own rules or after them. Your provider may offer groups too. Rules
apply on the host, in front of your server; your system's own firewall still
applies inside it.

## 9. Backups, snapshots and images

- **Backups** copy the whole server off its host. Take one now, or add a
  **schedule** (how often, and how many or how long to keep). Your plan may cap
  how many you keep. **Restore** writes a backup over the server's disks; stop the
  server first. If a backup fails after its last try, you
  are emailed.
- **Snapshots** freeze the disk as it is, on the same host, so you can revert
  quickly, for example before an upgrade. They are not backups: they go with
  the server. Servers on some storage cannot take them. Reverting makes the next
  backup a full one.
- **Images** (Images) capture a server's disk to keep. An image outlives the
  server and can seed a new one at order time.

## 10. Transfer and speed

Your plan may have a monthly transfer allowance; the Network tab shows this
month's use and past months. You are emailed as you near it. Past it, the
server may be slowed down, stopped until the month resets, or billed for the
extra, as your provider's plan says.

## 11. API keys and automation

**API Keys** makes keys (`mvk_...`) for your own scripts. A key acts as you and
reaches only your own servers. Send it as `X-API-Key: mvk_...` or
`Authorization: Bearer mvk_...`. The full key is shown once. A **read-only**
key can only read. The panel's API reference is at `/docs/api` on the panel,
and AI agents can use the panel's MCP endpoint with a key
([api.md](api.md)). Your provider may limit how many requests a minute a key
may make, or switch keys off.

## 12. When something goes wrong

| You see | What to do |
|---|---|
| The server does not answer, but its page says Running | Open the Console: the system may be stuck or its network misconfigured. Restart, or use rescue mode. |
| "State unknown" | The host is unreachable; your provider has been alerted. Nothing to do on your side. |
| "Suspended" | Contact your provider, usually about billing. |
| A note about an address change not in the guest yet | Restart the server from the panel, or wait for its guest agent. |
| Too many requests | You went over the request limit; wait the seconds the answer gives. |
| Account locked | Wait the time shown, or reset your password. |
