# Teely

Teely is a local serverless launcher for macOS web apps that tees them up with friendly hostnames, on-demand startup, and built-in HTTPS, with Caddy handling the rest.

![Teely flow](docs/teely-flow.png)

## Install

```bash
go install github.com/marksowell/teely/cmd/teely@latest
```

## Quick Start

```bash
teely init
# edit the generated config
teely up
teely trust
```

Then open:

- [https://teely.localhost](https://teely.localhost)

If you run Teely inside the repo, it creates `teely.local.json`. Otherwise it creates a user config under your macOS config directory.

## What Teely Does

- friendly local hostnames like `https://sample-app.localhost`
- starts apps on demand
- bridges cold starts automatically so first requests can complete cleanly
- shows a startup page while an app is booting
- routes traffic through local HTTPS with Caddy
- stops apps after idle timeout
- gives you a built-in dashboard for setup, status, logs, and controls

## Common Commands

```bash
teely up
teely status
teely restart
teely down
```

## Add Your Apps

1. Run `teely init`
2. Put in your real app paths, commands, hostnames, and ports
3. Start Teely with `teely up`
4. Open `https://teely.localhost` to manage apps

Keep real machine paths and local app details in your local Teely config, not in the checked-in sample config.
