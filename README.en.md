<p align="center">
  <picture>
    <source media="(max-width: 640px) and (prefers-reduced-motion: reduce)" srcset="docs/assets/readme/hero-mobile-en.png?v=03622fed91">
    <source media="(max-width: 640px)" srcset="docs/assets/readme/hero-mobile-en.gif?v=5bdf103a42">
    <source media="(prefers-reduced-motion: reduce)" srcset="docs/assets/readme/hero-en.png?v=fa51948a7c">
    <img src="docs/assets/readme/hero-en.gif?v=7e366284d4" width="100%" alt="Apophis TeamSeatWatch: Your Team. Everything in view. Accounts, seats, batch onboarding and card delivery.">
  </picture>
</p>

<h1 align="center">Apophis TeamSeatWatch</h1>

<p align="center">
  An open source project built for the community.<br>
  Manage near-expiry ChatGPT Teams, from accounts and seats to delivery.
</p>

<p align="center">
  <a href="README.md">简体中文</a> · English
</p>

<p align="center">
  <a href="#preview">Product</a> &nbsp; / &nbsp;
  <a href="#workflow">Workflow</a> &nbsp; / &nbsp;
  <a href="#start">Get started</a> &nbsp; / &nbsp;
  <a href="#community">Community</a> &nbsp; / &nbsp;
  <a href="https://linux.do/">LINUX DO</a>
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-Apache_2.0-4C4EDA?style=flat-square" alt="Apache 2.0 License"></a>
</p>

---

<a id="notice"></a>

## Important notes

> **Project identity**: TeamSeatWatch is an independent open source project and is not affiliated with OpenAI.
>
> **Authorized use**: Only manage accounts and workspaces you are authorized to use, and follow upstream terms of service and applicable laws.
>
> **Usage risks**: Account restrictions and changes to upstream rules may affect availability. The project is provided as is, with no guarantee of account quality or service continuity. See [LICENSE](LICENSE) for the software license and liability terms.
>
> **Protect your data**: Keep account credentials and card secrets secure, back up important data, and hide sensitive information in public feedback.

<p align="center"><strong>EXCLUSIVE SPONSOR</strong></p>

<a href="https://www.apophis.uk/">
  <picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/apophiscode-light-mobile-en.png?v=260a84508b">
    <img src="docs/assets/readme/apophiscode-light-en.png?v=0201901b12" width="100%" alt="ApophisCode exclusive sponsor: Claude Fable 5.1 full-power Max 1.4x; Claude Opus 5 Kiro 0.25x; GPT-6 Astra 0.1x. Welcome credit after joining the group and binding your account; daily check-in credit. Sponsor QQ group: 425421707. Click to visit the website.">
  </picture>
</a>

<p align="center">
  <sub>Promotional details supplied by the sponsor. Check its website and group for current models, multipliers and credit offers.</sub>
</p>

<a id="preview"></a>

## Product preview

Choose the owning account, target workspace and source batch, then follow five steps through invitations, OAuth sign-in, card generation and batch results.

<a href="docs/assets/readme/start-operation.png">
  <picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/start-operation-mobile-en.png?v=7457712b91">
    <img src="docs/assets/readme/start-operation-en.png?v=47ab55736b" width="100%" alt="Current Start Operation interface with fictional example data: five workflow steps, owning account, target workspace, source batch and planned removal time. Click for the original screenshot.">
  </picture>
</a>

<p align="center"><sub>Current Start Operation page with fictional example data. The application interface is currently primarily Chinese; this README and the deployment guide are bilingual.</sub></p>

- **Accounts & batches**: import, filter and check account status, then organize available accounts into clear batches.
- **Workspaces & onboarding**: review subscriptions, members and seats, then invite, authorize and generate cards for the selected batch.
- **Delivery & follow-up**: copy or export cards, review redemption and recovery records, then check and confirm rotation.

<a id="workflow"></a>

## Workflow

<picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/workflow-mobile-en.png?v=e997728a64">
    <img src="docs/assets/readme/workflow-en.png?v=f03c877403" width="100%" alt="Product architecture and workflow: operators prepare accounts, check seats, invite and authorize, then generate one card per account. Customers redeem and recover from a separate entry. Operators trace records, manually confirm rotation and prepare the next batch.">
  </picture>

1. **Prepare**: import and check accounts, then organize an available batch.
2. **Onboard**: choose the workspace and account list, then invite and authorize.
3. **Deliver**: generate, copy or export cards for customers to redeem.
4. **Follow up**: review redemption and recovery records, then check the scope and confirm rotation.

> **One card, one account. Multiple cards keep separate records.** 401 recovery retains the original account and workspace. Rotation requires manual confirmation and preserves existing delivery protections.

<a id="start"></a>

## Get started

Prepare a **Linux x86_64** host with Docker Engine (including Compose), Python 3 and OpenSSL. Run:

```bash
git clone https://github.com/xft0202/Apophis-TeamSeatWatch.git
cd Apophis-TeamSeatWatch
python3 deploy/manage.py start
```

The first run builds the app, prepares the configuration and guides you through setting an admin password. Wait for **“已就绪 / Ready”**, trust the local certificate as instructed, then open:

- **Admin workspace**: `https://localhost:18443/owner/`<br>
  Sign in with `owner` and your new password.
- **Customer redemption**: `https://localhost:18444/redeem/`<br>
  Redeem cards and recover from 401 errors.

**[Read the deployment & first-use guide →](docs/deployment.en.md)**<br>
These are local addresses on the deployment host. The guide covers remote access, a redemption domain and routine maintenance.

<a id="structure"></a>

## Project structure

<details>
<summary><strong>View directories and their purpose</strong></summary>

```text
Apophis-TeamSeatWatch/
├── deploy/          # Startup, deployment and maintenance
├── web/src/
│   ├── owner/       # Admin workspace
│   ├── public/      # Customer redemption and recovery
│   └── shared/      # Shared components and styles
├── cmd/             # Service entry point
├── internal/        # Accounts, seats, batches and delivery
├── api/openapi/     # API specifications
└── docs/            # Deployment guides and project visuals
```

</details>

<a id="community"></a>

## Community & feedback

<p>
<picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/community-exchange-mobile-en.png?v=c39113d6ee">
    <img src="docs/assets/readme/community-exchange-en.png?v=186e461566" width="100%" alt="Project QQ community: 1129084117. Share experience and everyday management tips. Search the group number in QQ to join.">
  </picture>
</p>

<p>
<a href="https://github.com/xft0202/Apophis-TeamSeatWatch/issues">
  <picture>
    <source media="(max-width: 640px)" srcset="docs/assets/readme/community-feedback-mobile-en.png?v=7e06a1c8d2">
    <img src="docs/assets/readme/community-feedback-en.png?v=fd1fbdac90" width="100%" alt="Problems and suggestions. Report a problem or suggest an improvement. Click to open GitHub Issues.">
  </picture>
</a>
</p>

<a id="stars"></a>

## Star History

If this project helps you, give it a Star to support its continued development.

<a href="https://www.star-history.com/?repos=xft0202%2FApophis-TeamSeatWatch&amp;type=date">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=xft0202/Apophis-TeamSeatWatch&amp;type=date&amp;theme=dark">
    <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=xft0202/Apophis-TeamSeatWatch&amp;type=date">
    <img src="https://api.star-history.com/chart?repos=xft0202/Apophis-TeamSeatWatch&amp;type=date" width="100%" alt="GitHub Star history for Apophis TeamSeatWatch. Click to view the detailed chart.">
  </picture>
</a>

---

<p align="center">
  <strong>Apophis TeamSeatWatch</strong><br>
  <sub>Open source project · Self-hosted · Improvements welcome</sub><br>
  <sub><a href="LICENSE">Apache License 2.0</a> · <a href="NOTICE">NOTICE</a> · Exclusively sponsored by <a href="https://www.apophis.uk/">ApophisCode</a></sub>
</p>
