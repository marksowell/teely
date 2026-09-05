# Teely

[![Go Reference](https://pkg.go.dev/badge/github.com/marksowell/teely.svg)](https://pkg.go.dev/github.com/marksowell/teely)

**Teely keeps your local web apps organized, reachable, and off until needed.**

Teely is a local app manager for web projects you develop, maintain, or run on your Mac. Register an app once with its project folder, startup command, port, and hostname.

After that, open a friendly HTTPS URL like `https://sample-app.localhost`. Teely starts the app on demand, waits for it to become ready, forwards the original request, and shuts it down after it goes idle.

Teely works with arbitrary local commands and does not require containers. Caddy handles local HTTPS and routing behind the scenes.

Think of Teely as **local scale-to-zero** or **serverless for localhost**. Teely keeps your local apps teed up and ready to use, starting their dev servers on demand and shutting them down when they’re no longer needed.

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
teely up
teely trust
```

Then open:

- [https://teely.localhost](https://teely.localhost)

From the dashboard you can:

- add and edit apps
- finish machine setup
- configure AI support for OpenAI, Anthropic, Google
- save AI keys into macOS Keychain

Once AI is configured, **Add App with AI** appears in the dashboard. Point it at a working project folder and Teely drafts the app registration for you.

## What Teely Does

- tracks local web apps in one dashboard with their paths, commands, ports, hostnames, status, and logs
- friendly local hostnames like `https://sample-app.localhost`
- starts apps on demand when they receive HTTP traffic
- keeps the original request alive through cold start instead of immediately failing it
- provides a startup page while a browser navigation is waiting for an app to boot
- routes traffic through local HTTPS with Caddy
- stops apps after idle timeout
- works with local app commands directly instead of requiring containers
- keeps configured app ports unique so registered apps do not accidentally route to each other
- refuses to proxy or manage external processes that are already using a registered app's port
- can draft app registrations with AI from an existing project folder
- gives you a built-in dashboard for app registration, setup, status, logs, and controls

## AI-Assisted App Import

Teely can inspect a project folder and draft the registration fields needed to run it locally. It reads common project files such as `README.md`, `package.json`, `Procfile`, `pyproject.toml`, compose files, and startup scripts, then combines local heuristics with your configured AI provider.

The app should already work outside Teely. AI import is meant to save the typing and catch the usual details:

- project name, app ID, and `.localhost` hostname
- working directory and startup command
- health check path and method
- idle and startup timeouts
- framework defaults such as Next.js using port `3000`

AI import also checks existing Teely app ports. When a drafted port is already assigned to another Teely app, it chooses the next available port if it can also update the command safely. For example, a Next.js app that would normally use `npm run dev` on port `3000` can be drafted as:

```bash
npm run dev -- -p 3001
```

with the app port set to `3001`. If Teely cannot confidently rewrite the command, it leaves the draft alone so the normal validation can catch the port conflict.

AI configuration lives in Teely setup. The provider and model are stored in Teely config, and API keys are securely stored separately in macOS Keychain.

## Related Projects

Closest projects in this space include [Coulson](https://github.com/ratazzi/coulson), [Tako](https://tako.sh/docs/development/), and [puma-dev](https://github.com/puma/puma-dev). Teely is aimed at the same wake-on-request local workflow, with a built-in dashboard and plain local app commands.

| Project | Start on request | Stop on idle | HTTPS / hostname | Arbitrary local process | macOS-oriented | UI |
| --- | --- | --- | --- | --- | --- | --- |
| **Teely** | ✅ | ✅ | ✅ Caddy + `.localhost` | ✅ | ✅ | ✅ |
| **[Coulson](https://github.com/ratazzi/coulson)** | ✅ | ✅ | ✅ local domains | Partial | Somewhat | ✅ |
| **[Tako](https://tako.sh/docs/development/)** | ✅ | ✅ | ✅ trusted HTTPS + `.test` | Partial | Somewhat | CLI |
| **[puma-dev](https://github.com/puma/puma-dev)** | ✅ | ✅ | ✅ HTTPS + local domains | Partial | ✅ | ❌ |

## Common Commands

```bash
teely up
teely status
teely restart
teely down
```

## Troubleshooting

Check status first:

```bash
teely status
```

If routing or HTTPS is acting up, restart both processes:

```bash
teely restart
```

Teely keeps logs under your configured `runtime_dir`.

Default install location:

- `~/Library/Application Support/Teely/.teely/logs/teely.log`
- `~/Library/Application Support/Teely/.teely/logs/caddy.log`

If you run Teely from a repo-local config instead, those logs live under that config's local `.teely/logs/` directory.

To temporarily enable Caddy debug logging, add `debug` to the top-level block in your generated Caddyfile, then restart Teely:

```caddy
{
	debug
	local_certs
	skip_install_trust
}
```

Remove `debug` again after you finish troubleshooting.

## License

Apache-2.0. See [LICENSE](LICENSE).
