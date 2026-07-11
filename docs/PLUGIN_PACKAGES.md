# WorkGround2 Plugin Packages

WorkGround2 plugin packages bundle skills, hooks, and MCP servers behind one
installable unit.

## CLI Mode

Use `WorkGround2 plugin` when installing or managing plugin packages from a
terminal. Plugin packages are installed globally under the WorkGround2 home
directory.

### Install From CLI

`install` accepts one source:

- A GitHub repository, such as `git:github.com/obra/superpowers` or
  `https://github.com/obra/superpowers`.
- A GitHub branch or subdirectory URL, such as
  `https://github.com/owner/repo/tree/main/path/to/plugin`.
- A local directory that contains `WorkGround2-plugin.json` or
  `.codex-plugin/plugin.json`.
- A local `.zip` archive that contains the plugin manifest at the archive root
  or under one top-level plugin directory.

Preview the install plan without writing files:

```bash
WorkGround2 plugin install git:github.com/obra/superpowers --dry-run
```

Install a plugin after reviewing the plan:

```bash
WorkGround2 plugin install git:github.com/obra/superpowers --yes
```

Install with an explicit name or replace an installed plugin with the same name:

```bash
WorkGround2 plugin install git:github.com/obra/superpowers --name superpowers --replace --yes
```

Use a local directory in developer mode:

```bash
WorkGround2 plugin install /path/to/plugin --link --replace --yes
```

Install a packaged AddOn archive:

```bash
WorkGround2 plugin install D:\Work\wg2addons\dist\draw-tool-0.1.0-windows-amd64.zip --yes
```

CLI install flags:

- `--dry-run` plans and validates the install without writing files.
- `--yes` is required for any install that writes files.
- `--replace` allows the source to replace an installed plugin with the same
  name.
- `--name <name>` or `--name=<name>` overrides the name from the plugin
  manifest for this install.
- `--link` links a local plugin directory instead of copying it into WorkGround2's
  plugin storage. Moving or deleting that directory breaks the linked plugin.

Running `WorkGround2 plugin install <source>` without `--dry-run` or `--yes`
refuses to write files and prints a reminder to rerun with one of those flags.
Install and remove commands print the structured JSON response from the same
install-source backend used by the desktop UI.

Installed plugin state is stored in:

```text
~/.WorkGround2/plugin-packages.json
~/.WorkGround2/plugins/<name>/
```

### Manage From CLI

List installed plugins:

```bash
WorkGround2 plugin list
```

Show one plugin's metadata, root, source, and exported capability counts:

```bash
WorkGround2 plugin show superpowers
```

Check that the manifest and skill roots are readable:

```bash
WorkGround2 plugin doctor superpowers
```

Enable or disable a plugin without uninstalling it:

```bash
WorkGround2 plugin disable superpowers
WorkGround2 plugin enable superpowers
```

Remove a plugin:

```bash
WorkGround2 plugin remove superpowers --yes
```

`remove` also accepts `uninstall` as an alias. It requires `--yes` because it
writes state and removes copied plugin content. For linked local plugins, the
external source directory is left in place.

## Desktop Settings

Open **Settings -> Plugins** to install and manage plugin packages without using
the CLI.

### Install Plugins

The installer has two modes:

- **Local zip/folder**: click **Choose plugin zip** and select a packaged plugin
  archive. For source debugging, click **Choose plugin folder** and select a
  plugin directory. The selected path is shown next to the buttons.
- **Git repository**: enter a Git source such as
  `git:github.com/obra/superpowers`. **Install name (optional)** can override
  the plugin manifest name for this install or overwrite.

Use the action buttons after choosing the source and options:

- **Preview** validates the source and shows the planned install actions without
  writing files.
- **Install plugin** installs the selected source using the current options.
- **Refresh plugins** reloads the installed-plugin list from disk and config.

Installer options:

- **Overwrite same-name plugin** allows the current source to replace an
  installed plugin with the same name. Leave it off when duplicate-name installs
  should fail instead of replacing existing content.
- **Developer mode: link selected folder** is available only for folder sources.
  It links the selected directory instead of copying it into WorkGround2's plugin
  storage. Use it while developing or debugging a plugin. Moving or deleting the
  selected directory will break the linked plugin.

Preview is the safest first step for a new Git source, local zip, or local plugin directory.

### Manage Installed Plugins

The installed-plugin list shows each plugin package and its exported skills,
hooks, and MCP servers. Use **Refresh plugins** after editing plugin files or
changing config outside the app.

Expand a plugin row to manage it:

- Enable or disable the plugin.
- **Update** pulls or refreshes an installed plugin when an update source is
  available.
- **Doctor** checks the plugin manifest and reports warnings or diagnostics.
- **Remove plugin** uninstalls the package after confirmation.

## Native Manifest

WorkGround2 plugins can declare `WorkGround2-plugin.json` at the plugin root:

```json
{
  "name": "example",
  "version": "1.0.0",
  "description": "Example plugin",
  "skills": "skills",
  "hooks": {
    "SessionStart": [
      {
        "command": "hooks/session-start",
        "description": "Load startup context"
      }
    ]
  },
  "mcpServers": {
    "helper": {
      "command": "bin/helper"
    }
  }
}
```

Relative paths are resolved inside the plugin root. WorkGround2 does not run
third-party install scripts during plugin installation.

## AddOn Packages

An AddOn package uses the same plugin package installer, then adds an `addon`
manifest block. To keep AddOns separately compiled, expose executable capability
through `mcpServers` and point `addon.runtime` at one of those servers:

```json
{
  "name": "draw-tool",
  "version": "0.1.0",
  "addon": {
    "kind": "draw-tool",
    "displayName": "Draw AddOn",
    "runtime": { "type": "mcp", "mcpServer": "draw-addon" },
    "panels": [{ "id": "providers", "title": "Providers", "entry": "panels/providers.schema.json" }],
    "configSchema": "panels/providers.schema.json",
    "storage": { "namespace": "draw-tool" }
  },
  "mcpServers": {
    "draw-addon": { "command": "bin/workground2-draw-addon", "auto_start": true }
  }
}
```

The host injects `WORKGROUND2_HOME`, `WorkGround2_HOME`,
`WORKGROUND2_PLUGIN_ROOT`, `WORKGROUND2_PLUGIN_NAME`,
`WORKGROUND2_ADDON_KIND`, `WORKGROUND2_ADDON_STORAGE_NAMESPACE`,
`WORKGROUND2_ADDON_HOME`, `WORKGROUND2_ADDON_CONFIG_DIR`,
`WORKGROUND2_ADDON_DATA_DIR`, and `WORKGROUND2_ADDON_STATE_DIR` into package MCP
servers. Secret declarations are metadata only; plaintext is not injected.

Build the reference Draw AddOn package:

```powershell
D:\Work\wg2addons\scripts\build-addons.ps1
```

`.\scripts\build-addons.ps1` in the main WorkGround2 repo is a wrapper for the
external AddOn root. On systems with `make`, `make build-addons` runs the same
external build. The build writes the runtime binary under
`D:\Work\wg2addons\draw-tool\bin\` and emits distributable archives under
`D:\Work\wg2addons\dist\`, for example:

```text
D:\Work\wg2addons\dist\draw-tool-0.1.0-windows-amd64.zip
```

The archive can be installed with `WorkGround2 plugin install <zip> --yes`.
During session boot, WorkGround2 expands the installed package metadata, injects
the AddOn environment variables into the package MCP server, and registers the
server's tools without compiling the AddOn runtime into the main binary.

## Codex Compatibility

WorkGround2 also reads Codex plugin manifests at `.codex-plugin/plugin.json`.
For packages such as Superpowers, WorkGround2 maps:

- `skills` to WorkGround2 skill roots.
- `hooks/session-start-codex` to the WorkGround2 `SessionStart` hook when present.

Plugin hooks receive these environment variables:

- `WorkGround2_PLUGIN_ROOT`
- `WorkGround2_PLUGIN_NAME`
- `WorkGround2_PLUGIN_VERSION`
- `WorkGround2_HOME`
- `WorkGround2_WORKSPACE_ROOT`

## Desktop Backend Methods

Desktop exposes plugin package operations through Wails methods:

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`
