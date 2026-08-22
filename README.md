# Teely

[![Go Reference](https://pkg.go.dev/badge/github.com/marksowell/teely.svg)](https://pkg.go.dev/github.com/marksowell/teely)

**Teely - Local apps that run only when you use them.**

Teely automatically starts local web applications when they receive an HTTP request, waits for them to become ready, forwards the original request through the cold start, and shuts them down again after they go idle.

It gives your apps friendly `.localhost` URLs and automatic local HTTPS with Caddy, without requiring containers or app-specific launchers. Teely works with arbitrary local commands, so it can replace manually starting and stopping Node.js, Python, Rails, and similar development servers.

Think of it as **local scale-to-zero** or **serverless for localhost**: you can keep many local apps available without keeping all of their dev servers running all the time.

Conceptually:

`request -> Teely starts the app if needed -> waits for readiness -> forwards the request -> shuts the app down after idle`

![Teely flow](docs/teely-flow.png)

![Teely dashboard](docs/teely-dashboard.png)

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

## What Teely Does

- friendly local hostnames like `https://sample-app.localhost`
- starts apps on demand when they receive HTTP traffic
- keeps the original request alive through cold start instead of immediately failing it
- provides a startup page while a browser navigation is waiting for an app to boot
- routes traffic through local HTTPS with Caddy
- stops apps after idle timeout
- works with local app commands directly instead of requiring containers
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
