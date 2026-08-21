# Teely

Teely is a local app launcher for macOS with friendly hostnames, on-demand startup, and built-in HTTPS.

Teely tees up your local apps, Caddy handles the rest.

![Teely flow](docs/teely-flow.png)

## Quick Start

```bash
./scripts/up.sh
./scripts/trust-caddy.sh
```

Then open:

- [https://teely.localhost](https://teely.localhost)

If `teely.local.json` does not exist yet, the first run creates it and asks you to edit it before continuing.

## What Teely Does

- friendly local hostnames like `https://sample-app.localhost`
- starts apps on demand
- shows a startup page while an app is booting
- routes traffic through local HTTPS with Caddy
- stops apps after idle timeout
- gives you a built-in dashboard for setup, status, logs, and controls

## Common Commands

```bash
./scripts/up.sh
./scripts/status.sh
./scripts/restart.sh
./scripts/down.sh
```

## Add Your Apps

1. Copy `teely.json` to `teely.local.json`
2. Put in your real app paths, commands, hostnames, and ports
3. Start Teely with `./scripts/up.sh`
4. Open `https://teely.localhost` to manage apps

Keep real machine paths and local app details in `teely.local.json`, not in the checked-in sample config.
