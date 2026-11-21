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
13. [Teams and projects](#13-teams-and-projects)
14. [Recipes and the app catalog](#14-recipes-and-the-app-catalog)

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

- **Your details**: name, company, phone, time zone and the language dates and
  numbers are written in. Change your email (needs your current password) and
  password below. Changing your password signs you out everywhere.
- **Two-step sign-in**: scan the code with an authenticator app, enter a code
  to confirm, and keep the backup codes somewhere safe. Each backup code signs
  you in once if you lose the phone. Turning it off needs a fresh code.
- **Sessions**: every place you are signed in, with where and when. Sign one
  out, or "sign out everywhere else".
- **Sign-in history**: every attempt on your account, good or bad.
- **Email**: whether you get news and offers by email. Notices about your
  servers (planned maintenance, an incident) reach you either way. The link at
  the bottom of a news email turns news off too.

**SSH Keys**: public keys you can install on new servers. Add one you have, or
let the panel generate a pair (the private key is shown once and not kept).

**Notifications** (the bell): your server was suspended or restored, a backup
failed, your transfer reached a warning level, your password or two-step
sign-in changed, an API key was made, a request for an address was answered.
The same events are emailed to you. **Notifications** in the menu lists every
one from the last 90 days, a page at a time.

**Help** in the menu links to your provider's support, billing and service
status pages, when they have set them.

**Announcements** from your provider, such as planned maintenance on the host
your server runs on, appear at the top of your Dashboard while they apply. Hide
one once you have read it. While the panel itself is under maintenance, a
banner says so: you can look around, but changes wait until it ends, and your
servers keep running.

## 3. Ordering a server

**Order VM** (if your provider allows ordering here; otherwise order through
their billing area):

0. **Account** (only when you work in a team, see section 13): your own
   account or a team's, and optionally one of its projects.
1. **Location.** Where the server runs. It cannot move to another location
   later.
2. **Plan.** The size: CPU, memory, disk, speed, monthly transfer, what each
   server may keep (snapshots, backups, extra addresses, disks), and the price
   if your provider shows one. The summary lists the price of the extras and
   links to your billing account.
3. **Operating system**, or one of your images.
4. **Hostname.**
5. **Network** (optional): a public address, or one of your private networks in
   that location.
6. **Apps and first-boot script** (optional, Linux): one of your provider's
   apps, one of your recipes, or a script you paste. It runs once, as root,
   on the first boot. An app may ask for values such as a domain name.
7. **Extras** (optional, when the plan includes them): **IPv6**, a routed /64
   beside the IPv4 address (the order says so if the location has none), and
   **automatic backups**, daily or weekly.
8. **SSH keys** (optional, Linux): installed for `root`.

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
| Settings | Name, tags, lock, reinstall, rescue, ISO, extra disks, clone, delete. |

**Power.** Start, Stop (asks the system to shut down), Restart and Power off
(cuts power, like pulling the plug). A server that is **suspended** by your
provider cannot be started until they restore it; usually a billing matter.

**Extra disks** (if allowed): Settings, Add disk. **Detaching a disk deletes
it** and everything on it. An extra disk can also grow (`POST
/api/v1/vms/{id}/disks/{device}/resize`), while the server runs too; disks never
shrink. A grown disk is bigger at once, but its partition and filesystem are
not: grow them inside the server (on Linux `growpart` then `resize2fs` or
`xfs_growfs`, which cloud images do at their next start; on Windows, extend
the volume in Disk Management). Your system disk grows with your plan.

**Clone** (if allowed) makes a copy of the server as a new one.

**Tags** are your own labels (such as `web` or `env:prod`, up to 10); the
list of servers can be filtered by them. **Lock** protects a server from being
deleted or reinstalled by mistake, until you unlock it. Your provider can also
lock a server; only they can unlock that.

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
- **Reset password** sets a new `root` or `Administrator` password. A running
  server takes it through its guest agent. A stopped Linux server has it
  written onto its disk and stays stopped; it works from the next start, so
  this is the way in when the guest agent does not answer. A stopped router
  takes it when it next starts; on RouterOS 7 a running router restarts to
  apply it. Windows takes one only while it runs.

## 6. Reinstall, rescue and ISOs

- **Reinstall** (Settings) erases the disk and installs the system you choose,
  of any kind; addresses and the console password stay. A new login password
  is shown once. Windows needs at least 2 GB of memory and a 32 GB disk.
- **Rescue mode** boots a stopped server from a rescue system so you can repair
  its disk. Where your provider set up the built-in rescue system, you are shown
  a one-time `root` password (once: copy it), and starting the server boots a
  small Linux system on the server's own address, reachable over SSH (if your
  firewall lets port 22 in) or the console, with your server's disks attached
  for repair (`lsblk` lists them). Otherwise it boots a rescue ISO, used from
  the console. **Leave rescue** (server stopped) boots it normally again.
  Backups, snapshots and reinstalls wait until it is out of rescue mode. If the
  button is disabled, rescue is not offered.
- **Attach ISO**: pick one of the installers your provider lists, or (if they
  allow it) give the address of your own. Your own address must be public;
  very large files and private addresses are refused. Detach it when done. A
  server has two CD drives: drive 1 boots first while it holds a disc, drive 2
  is for drivers or files. A running server takes a disc at once in a drive it
  already has; otherwise stop it first.
- **Install from an ISO**: when your provider offers ISOs as a system to order,
  the server starts with an empty disk and the installer in drive 1. Install
  it from the console; nothing is set up for you and no password is generated.
  For Windows, the drivers disc in drive 2 lets the installer see the disk.
  Unmount the installer once the system is installed.

## 7. Networking

**More addresses.** On the Network tab, **Request address** asks for another
IPv4 address, or IPv6 if the server has none, within what its plan includes.
Depending on your provider it is assigned straight away or after staff look
at it; either way you are notified. The server is not restarted. Withdraw a
request while it waits.

- **Addresses.** The Network tab lists your server's IPv4 and IPv6 addresses.
  If your provider adds or changes one, the server takes it at once when its
  guest agent can apply it, otherwise at its next start from the panel; a note
  on the page says which.
- **Reverse DNS**: set the name each of your addresses answers to. With an
  IPv6 prefix, name any address inside it (up to 64), for example the one your
  mail server sends from. The name must already point to the address.
- **IPv6.** With an IPv6 prefix, **More addresses in** gives the server up to
  16 further addresses from it besides its `::1`; the server takes them like
  any address change. **Turn off** takes IPv6 away from the server; ask for it
  again with **Request address**.
- **Private networks** (Private Networks): your own subnets, for servers to
  talk privately. Each lives in one location; servers join one there. You may
  use any private range, even one other customers use. **Nameservers** sets
  the resolvers servers in it are given when they are created there,
  reinstalled or readdressed.
- **Network cards.** A server ordered with a public address can join one of
  your private networks in its location as well: **Add card** on the Network
  tab adds a second card with an address from that network. It appears at
  once where the server's machine allows, otherwise at its next start from the
  panel; the server is not restarted for it. Remove the card the same way.
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
  how many you keep (the Backups tab says how many, and the price per backup
  when your provider shows one). **Restore** writes a backup over the server's
  disks; stop the server first. If a backup fails after its last try, you
  are emailed.
- **Download** saves a full backup's disk image (qcow2), one link per disk. A
  backup marked encrypted is decrypted as it downloads. An incremental backup
  holds only changes, so download the full one it builds on; a backup the panel
  cannot reach says so and why. A read-only team member cannot download.
- **Snapshots** freeze the server as it is, on the same host, so you can revert
  quickly, for example before an upgrade. They are not backups: they go with
  the server. Servers on some storage cannot take them. A snapshot of a running
  server also saves its memory, which pauses it while that is written; tick
  **Disks only** to save the disks alone without a pause (and **Freeze the
  filesystems first** to have the guest agent make them consistent). Reverting
  puts the server back as it was: running where it left off, or stopped; after
  a disks-only snapshot it boots from the snapshot's disks. Reverting makes the
  next backup a full one.
- **Snapshot schedule** (Snapshots tab): a snapshot every 6 hours to every week,
  keeping the newest few it took; the oldest goes when a new one is taken.
  Snapshots you take yourself are never removed by it, and count against your
  plan's snapshot allowance, which the schedule has to fit in.
- **Images** (Images) capture a server's disk to keep. An image outlives the
  server and can seed a new one at order time.

## 10. Transfer and speed

Your plan may have a monthly transfer allowance; the Network tab shows this
period's use and past ones. The period starts on the day your provider set,
and the plan may count only traffic your server sends, or only what it
receives; the Network tab says which. Your provider may also pool the
allowances of your servers, so they share one total. You are emailed as you
near it. Past it, the server may be slowed down, stopped until the allowance
starts again, or billed for the extra, as your provider's plan says.

If your server is attacked, or sends traffic that looks like an attack (often
a sign it was broken into), your provider may drop traffic to it for a while,
slow it down or cut it off from the network. The server keeps running and its
console still works; you are emailed what was done and when it is lifted.

## 11. API keys and automation

**API Keys** (under Settings) makes keys (`mvk_...`) for your own scripts. A
key acts as you and reaches only your own servers. Send it as
`X-API-Key: mvk_...` or `Authorization: Bearer mvk_...`. The full key is shown
once. When you make one you choose:

- what it may do: **Full access** (everything you can do), **Read only** (look,
  never change), or **Power only** (list your servers and start, shut down or
  restart them, nothing else);
- optionally, the addresses it works from (one address or range per line);
  requests from anywhere else are refused, and such a key cannot make keys or
  change your account settings;
- optionally, when it stops working.

Revoke a key you no longer use. The panel's API reference is at `/docs/api` on
the panel, and AI agents can use the panel's MCP endpoint with a key; it lists
only the tools that key may use ([api.md](api.md)). Your provider may limit
how many requests a minute a key may make, or switch keys off.

## 12. When something goes wrong

| You see | What to do |
|---|---|
| The server does not answer, but its page says Running | Open the Console: the system may be stuck or its network misconfigured. Restart, or use rescue mode. |
| "State unknown" | The host is unreachable; your provider has been alerted. Nothing to do on your side. |
| "Suspended" | Contact your provider, usually about billing. |
| A note about an address change not in the guest yet | Restart the server from the panel, or wait for its guest agent. |
| Too many requests | You went over the request limit; wait the seconds the answer gives. |
| Account locked | Wait the time shown, or reset your password. |

## 13. Teams and projects

**Team** (`/client/team`) lets colleagues into your account with their own
sign-in and two-step sign-in, instead of sharing yours. The servers, quota
and billing stay your account's.

- **Invite** by email, with a role:

  | Role | May |
  |---|---|
  | Admin | Everything you do, including the team and projects. |
  | Technical | Run the servers: power, console, reinstall, backups, networking, ordering; manage the account's images, floating IPs, VPCs, firewall groups, SSH keys and recipes. |
  | Billing | See the account's usage and billing only; not the servers or their resources. |
  | Read-only | See the servers and the account's resources; change nothing. |

  and the servers they reach: every server, chosen servers, or chosen
  projects. The email link works for 7 days. Someone without an account
  chooses a password when they accept; that account can work in your team
  but cannot order servers of its own until your provider allows it.
- **Change** a member's role or reach, **Remove** them, or **Revoke** an
  invitation not yet accepted. It takes effect at once.
- **Projects** group servers. Move a server into one from its Settings tab.
  A member limited to projects reaches the servers in them, and orders new
  servers into one of them.

If you work in someone else's account, its servers are in **My VMs** next to
your own (filter by **Account**), and **Team** lists the accounts you work in,
where you can **Leave** one. What a server's page lets you do follows your
role there: a read-only member sees the readings and settings but no power
buttons, console or passwords. A billing member sees the account's usage
against its limits on **Team** instead.

The account's own **Images**, **Floating IPs**, **VPCs**, **Firewall groups**,
**SSH keys** and **Recipes** pages show an **Account** picker when you work in
a team: choose the team's account to see its resources, and, with the Admin or
Technical role, to add or remove them. A member limited to chosen servers or
projects works on those servers only and does not see these pages for the
team's account. When ordering into a team's account you can pick its SSH keys
and recipes or your own.

## 14. Recipes and the app catalog

**Recipes** (`/client/recipes`) are scripts that run once when a new server
first boots. Save your own here to pick them on the order page; only you see
them. **App catalog** lists the apps your provider publishes, with the values
each asks for and the script it runs (open **Show script** to read it).

