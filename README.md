<p align="center">
  <img src="public/icon.png" alt="Netcatty" width="128" height="128">
</p>

<h1 align="center">Netcatty</h1>

<p align="center">
  <strong>Modern SSH Client, SFTP Browser & Terminal Manager</strong>
</p>

<p align="center">
  A beautiful, feature-rich SSH workspace built with Electron, React, and xterm.js.<br/>
  Host management, split terminals, SFTP, port forwarding, and cloud sync — all in one.
</p>

<p align="center">
  <a href="https://github.com/user/netcatty/releases/latest"><img alt="GitHub Release" src="https://img.shields.io/github/v/release/user/netcatty?style=for-the-badge&logo=github&label=Release"></a>
  &nbsp;
  <a href="#"><img alt="Platform" src="https://img.shields.io/badge/Platform-macOS%20%7C%20Windows%20%7C%20Linux-blue?style=for-the-badge&logo=electron"></a>
  &nbsp;
  <a href="LICENSE"><img alt="License" src="https://img.shields.io/badge/License-GPL--3.0-green?style=for-the-badge"></a>
</p>

<p align="center">
  <a href="https://ko-fi.com/binaricat">
    <img src="https://cdn.ko-fi.com/cdn/kofi3.png?v=2" width="150" alt="Support on Ko-fi">
  </a>
</p>

---

[![Netcatty Main Interface](screenshots/main-window-dark.png)](screenshots/main-window-dark.png)

---

# Contents <!-- omit in toc -->

- [What is Netcatty](#what-is-netcatty)
- [Features](#features)
- [Screenshots](#screenshots)
  - [Host Management](#host-management)
  - [Terminal](#terminal)
  - [SFTP](#sftp)
  - [Keychain](#keychain)
  - [Port Forwarding](#port-forwarding)
  - [Cloud Sync](#cloud-sync)
  - [Themes & Customization](#themes--customization)
- [Supported Distros](#supported-distros)
- [Getting Started](#getting-started)
- [Build & Package](#build--package)
- [Tech Stack](#tech-stack)
- [Contributing](#contributing)
- [License](#license)

---

<a name="what-is-netcatty"></a>
# What is Netcatty

**Netcatty** is a modern, cross-platform SSH client and terminal manager designed for developers, sysadmins, and DevOps engineers who need to manage multiple remote servers efficiently.

- **Netcatty is** an alternative to PuTTY, Termius, SecureCRT, and macOS Terminal.app for SSH connections
- **Netcatty is** a powerful SFTP client with dual-pane file browser
- **Netcatty is** a terminal workspace with split panes, tabs, and session management
- **Netcatty is not** a shell replacement — it connects to remote shells via SSH/Telnet or local terminals

---

<a name="features"></a>
# Features

### 🖥️ Terminal & Sessions
- **xterm.js-based terminal** with GPU-accelerated rendering
- **Split panes** — horizontal and vertical splits for multi-tasking
- **Tab management** — multiple sessions with drag-to-reorder
- **Session persistence** — restore sessions on restart
- **Broadcast mode** — type once, send to multiple terminals

### 🔐 SSH Client
- **SSH2 protocol** with full authentication support
- **Password & key-based authentication**
- **SSH certificates** support
- **Jump hosts / Bastion** — chain through multiple hosts
- **Proxy support** — HTTP CONNECT and SOCKS5 proxies
- **Agent forwarding** — including OpenSSH Agent and Pageant
- **Environment variables** — set custom env vars per host

### 📁 SFTP
- **Dual-pane file browser** — local ↔ remote or remote ↔ remote
- **Drag & drop** file transfers
- **Queue management** for batch transfers
- **Progress tracking** with transfer speed

### 🔑 Keychain
- **Generate SSH keys** — RSA, ECDSA, ED25519
- **Import existing keys** — PEM, OpenSSH formats
- **SSH certificates** support
- **Identity management** — reusable username + auth combinations
- **Export public keys** to remote hosts

### 🔌 Port Forwarding
- **Local forwarding** — expose remote services locally
- **Remote forwarding** — expose local services remotely
- **Dynamic forwarding** — SOCKS5 proxy
- **Visual tunnel management**

### ☁️ Cloud Sync
- **End-to-end encrypted sync** — your data is encrypted before leaving your device
- **Multiple providers** — GitHub Gist, S3-compatible storage, WebDAV, Google Drive, OneDrive
- **Sync hosts, keys, snippets, and settings**

### 🎨 Themes & Customization
- **Light & Dark mode**
- **Custom accent colors**
- **50+ terminal color schemes**
- **Font customization** — JetBrains Mono, Fira Code, and more
- **i18n support** — English, 简体中文, and more

---

<a name="screenshots"></a>
# Screenshots

<a name="host-management"></a>
## Host Management

The Vault view is your command center for managing all SSH connections. Create hierarchical groups with right-click context menus, drag hosts between groups, and use breadcrumb navigation to quickly traverse your host tree. Each host displays its connection status, OS icon, and quick-connect button. Switch between grid and list views based on your preference, and use the powerful search to filter hosts by name, hostname, tags, or group.

| Dark Mode | Light Mode | List View |
|-----------|------------|-----------|
| ![Dark](screenshots/main-window-dark.png) | ![Light](screenshots/main-window-light.png) | ![List](screenshots/main-window-dark-list.png) |

<a name="terminal"></a>
## Terminal

Powered by xterm.js with WebGL acceleration, the terminal delivers a smooth, responsive experience. Split your workspace horizontally or vertically to monitor multiple sessions simultaneously. Enable broadcast mode to send commands to all terminals at once — perfect for fleet management. The theme customization panel offers 50+ color schemes with live preview, adjustable font size, and multiple font family options including JetBrains Mono and Fira Code.

| Split Windows | Theme Customization |
|---------------|---------------------|
| ![Split](screenshots/split-window.png) | ![Theme](screenshots/terminal-theme-change.png) |

![Terminal Themes](screenshots/terminal-theme-change-2.png)

<a name="sftp"></a>
## SFTP

The dual-pane SFTP browser supports local-to-remote and remote-to-remote file transfers. Navigate directories with single-click, drag files between panes, and monitor transfer progress in real-time. The interface shows file permissions, sizes, and modification dates. Queue multiple transfers and watch them complete with detailed speed and progress indicators. Context menus provide quick access to rename, delete, download, and upload operations.

![SFTP View](screenshots/sftp.png)

<a name="keychain"></a>
## Keychain

The Keychain is your secure vault for SSH credentials. Generate new RSA, ECDSA, or ED25519 keys with customizable bit lengths, or import existing keys in PEM/OpenSSH format. Support for SSH certificates enables secure authentication with certificate authorities. Create reusable identities that combine username, authentication method, and keys — then assign them to multiple hosts. Export public keys directly to remote servers with one click, using customizable deployment scripts.

![Key Manager](screenshots/key-manager.png)

<a name="port-forwarding"></a>
## Port Forwarding

Set up SSH tunnels with an intuitive visual interface. Local forwarding exposes remote services (like databases or internal APIs) on your local machine. Remote forwarding shares your local services with remote servers. Dynamic forwarding creates a SOCKS5 proxy for secure browsing. Each tunnel shows real-time status with clear indicators for active, connecting, or error states. Save tunnel configurations for quick reuse across sessions.

![Port Forwarding](screenshots/port-forwadring.png)

<a name="cloud-sync"></a>
## Cloud Sync

Keep your hosts, keys, snippets, and settings synchronized across all your devices with end-to-end encryption. Your master password encrypts all data locally before upload — the cloud provider never sees plaintext. Choose from multiple storage backends: GitHub Gist for simplicity, S3-compatible storage (AWS, MinIO, Cloudflare R2) for flexibility, WebDAV for self-hosted solutions, or Google Drive and OneDrive for convenience. Conflict resolution ensures you never lose data during simultaneous edits.

![Cloud Sync](screenshots/cloud-sync.png)

<a name="themes--customization"></a>
## Themes & Customization

Make Netcatty truly yours with extensive customization options. Toggle between light and dark modes, or let the app follow your system preference. Pick any accent color to match your style. The application supports multiple languages including English and 简体中文, with more translations welcome via community contributions. All preferences sync across devices when cloud sync is enabled, so your personalized experience follows you everywhere.

![Themes & i18n](screenshots/app-themes-i18n.png)

---

<a name="supported-distros"></a>
# Supported Distros

Netcatty automatically detects and displays OS icons for connected hosts:

<p align="center">
  <img src="public/distro/ubuntu.svg" width="48" alt="Ubuntu" title="Ubuntu">
  <img src="public/distro/debian.svg" width="48" alt="Debian" title="Debian">
  <img src="public/distro/centos.svg" width="48" alt="CentOS" title="CentOS">
  <img src="public/distro/fedora.svg" width="48" alt="Fedora" title="Fedora">
  <img src="public/distro/arch.svg" width="48" alt="Arch Linux" title="Arch Linux">
  <img src="public/distro/alpine.svg" width="48" alt="Alpine" title="Alpine">
  <img src="public/distro/amazon.svg" width="48" alt="Amazon Linux" title="Amazon Linux">
  <img src="public/distro/redhat.svg" width="48" alt="Red Hat" title="Red Hat">
  <img src="public/distro/rocky.svg" width="48" alt="Rocky Linux" title="Rocky Linux">
  <img src="public/distro/opensuse.svg" width="48" alt="openSUSE" title="openSUSE">
  <img src="public/distro/oracle.svg" width="48" alt="Oracle Linux" title="Oracle Linux">
  <img src="public/distro/kali.svg" width="48" alt="Kali Linux" title="Kali Linux">
</p>

---

<a name="getting-started"></a>
# Getting Started

### Prerequisites
- Node.js 18+ and npm
- macOS, Windows 10+, or Linux

### Development

```bash
# Clone the repository
git clone https://github.com/user/netcatty.git
cd netcatty

# Install dependencies
npm install

# Start development mode (Vite + Electron)
npm run dev
```

### Project Structure

```
├── App.tsx                 # Main React application
├── components/             # React components
│   ├── Terminal.tsx        # Terminal component
│   ├── SftpView.tsx        # SFTP browser
│   ├── VaultView.tsx       # Host management
│   ├── KeyManager.tsx      # SSH key management
│   └── ...
├── application/            # State management & i18n
├── domain/                 # Domain models & logic
├── infrastructure/         # Services & adapters
├── electron/               # Electron main process
│   ├── main.cjs            # Main entry
│   └── bridges/            # IPC bridges
└── public/                 # Static assets & icons
```

---

<a name="build--package"></a>
# Build & Package

```bash
# Build for production
npm run build

# Package for current platform
npm run pack

# Package for specific platforms
npm run pack:mac     # macOS (DMG + ZIP)
npm run pack:win     # Windows (NSIS installer)
npm run pack:linux   # Linux (AppImage, deb, rpm)
```

---

<a name="tech-stack"></a>
# Tech Stack

| Category | Technology |
|----------|------------|
| Framework | Electron 39 |
| Frontend | React 19, TypeScript |
| Build Tool | Vite 7 |
| Terminal | xterm.js 5 |
| Styling | Tailwind CSS 4 |
| SSH/SFTP | ssh2, ssh2-sftp-client |
| PTY | node-pty |
| Icons | Lucide React |

---

<a name="contributing"></a>
# Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

See [agents.md](agents.md) for architecture overview and coding conventions.

---

<a name="license"></a>
# License

This project is licensed under the **GPL-3.0 License** - see the [LICENSE](LICENSE) file for details.

---

<p align="center">
  Made with ❤️ by <a href="https://ko-fi.com/binaricat">binaricat</a>
</p>
