# Start TeamSeatWatch

[Back to the project](../README.en.md) · [简体中文](deployment.md) / English

This route supports a **single Linux x86_64 host**. Install Docker Engine (including Compose), Python 3 and OpenSSL, and make sure your user can run Docker.

## 1. Start

```bash
git clone https://github.com/xft0202/Apophis-TeamSeatWatch.git
cd Apophis-TeamSeatWatch
python3 deploy/manage.py start
```

The first run downloads build dependencies. Wait for the build, then set an admin password of at least **14 characters** at the prompts. Password entry is hidden. **“已就绪 / Ready”** confirms installation is complete.

The tool prepares the database, encryption material, certificates and two entry points. Later runs reuse your data and admin account. Installation material lives in `~/.local/share/apophis-teamseatwatch`, separate from the source. If you have a manual installation, retain its backup first: this entry creates a separate installation and does not adopt old containers or import their data automatically.

The build stages the fixed SHA256 official ShardX 152.0.7977.65 ZIP in `.browser-artifacts/`; the runtime image contains only the unmodified extracted engine. Before a direct `docker build`, run `python3 tools/prepare-browser.py` (use `--source /path/to/ShardX-Linux.zip` to reuse the exact archive). Account profiles are created lazily and retained in the control service’s `browser-profiles` volume at `/var/lib/teamseatwatch/browser-profiles`. Restart and container recreation reuse this volume. Idle profiles run no browser. Fresh global concurrency defaults to five; saved proxy runtime parameters remain authoritative.

## 2. Open your workspace

Import the displayed `installation/tls/authority/ca.crt` into your browser or operating system's trusted root store. Import the `.crt` certificate only.

| Entry | Address | First action |
| --- | --- | --- |
| Admin workspace | `https://localhost:18443/owner/` | Sign in with `owner` and your new password |
| Customer redemption | `https://localhost:18444/redeem/` | Enter cards to redeem or recover after a 401 |

After signing in, choose a direct connection or configure a working proxy in Proxy Management, then follow the [homepage workflow](../README.en.md#workflow) to prepare accounts, a workspace and a batch. Successful startup confirms service readiness; real account onboarding results appear during operations.

<details>
<summary><strong>Install on a server and open it from your computer</strong></summary>

Forward both entry ports on your computer (replace the SSH host):

```bash
ssh -N -L 18443:127.0.0.1:18443 -L 18444:127.0.0.1:18444 user@your-server
```

Copy the server's displayed CA certificate to your computer, trust it, then open the same `localhost` addresses. All services bind to the host's local addresses by default.

For remote customer redemption, configure your existing domain proxy to forward a trusted HTTPS domain to `https://localhost:18444`. Verify the upstream with this installation's CA and use `localhost` as the upstream server name. Keep the admin entry local or use SSH forwarding. Retain the gateway, database and internal listener settings.

</details>

<details>
<summary><strong>Maintenance: status, restart, updates and backups</strong></summary>

Run from the source directory:

```bash
python3 deploy/manage.py status
python3 deploy/manage.py logs
python3 deploy/manage.py stop
python3 deploy/manage.py start
```

Forgot your password? Run `python3 deploy/manage.py reset-password`. Existing login sessions are revoked.

Update with `git pull --ff-only`, then `python3 deploy/manage.py start --build`. The tool builds first, stops the application, backs up, migrates and starts it. Failures retain your installation material; inspect logs and resume the same installation.

Run `python3 deploy/manage.py backup` to pause the application and save the database together with its encryption material, certificates and configuration under the data directory's `backups/`. Run `start` afterward to resume. A backup is also made before migration on every start.

Copy complete backups to a separate location. Recovery needs the matching `database.dump` and `installation.tar.gz` from the same backup. The database and encryption keys must stay together; generating replacement keys cannot restore encrypted records. Retain the original data directory and Compose project identity. Restarts and updates reuse the persistent database volume.

The local CA lasts 10 years and service certificates last 1 year. Before expiry, renew leaf certificates with the original CA, keep the keyring unchanged and restart the services.

</details>

<details>
<summary><strong>Ports, custom directories and startup problems</strong></summary>

On the first installation, choose a separate data directory and the start of six consecutive ports:

```bash
python3 deploy/manage.py start --data-dir /your/private/tsw-data --port-base 19440
```

This gives `https://localhost:19443/owner/` and `https://localhost:19444/redeem/`. Use the same `--data-dir` on every later command; the stored ports are reused. The defaults are 18440–18445.

| Problem | Next step |
| --- | --- |
| Build interrupted or dependencies fail to download | Check connectivity, then rerun the same `start` |
| Startup check fails | Run `logs`; follow the actual port, database or certificate error |
| Browser certificate error | Trust the CA on the browsing device and use the `localhost` address |
| Login or actions return 403 | Use the exact displayed admin address and sign in again |
| Platform login or probing fails | Read the account's result; check materials, permissions and proxy settings |

Project help: **QQ 1129084117**. Report reproducible problems in [GitHub Issues](https://github.com/xft0202/Apophis-TeamSeatWatch/issues) with redacted logs.

</details>
