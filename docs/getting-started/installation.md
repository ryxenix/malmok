# Installation

Malmok is distributed as a static binary for Linux and macOS on amd64 and
arm64. The release binary is the recommended installation method because it
contains the exact version reported by `malmok --version`.

## Install the latest release

The same command works from Bash and Fish:

```bash
curl -fsSL https://malmok.dev/install.sh | sh
```

The installer:

1. detects the operating system and processor architecture;
2. resolves the latest GitHub release;
3. downloads the matching binary and `SHA256SUMS`;
4. refuses to install when the checksum does not match; and
5. installs `malmok` into `/usr/local/bin`, using `sudo` only when required.

## Inspect before running

If your policy does not permit piping a remote script to a shell, download and
read it first:

```bash
curl -fsSLo install-malmok.sh https://malmok.dev/install.sh
less install-malmok.sh
sh install-malmok.sh
```

## Select a version or destination

Options follow `sh -s --` when the installer is piped:

```bash
# Install a specific release
curl -fsSL https://malmok.dev/install.sh | sh -s -- --version v0.83.0

# Install without elevated privileges
curl -fsSL https://malmok.dev/install.sh | sh -s -- --bin-dir ~/.local/bin

# Install the Linux binary that carries Helm and k9s for an air-gapped site
curl -fsSL https://malmok.dev/install.sh | sh -s -- --airgap
```

!!! note

    `--airgap` describes the Malmok operator binary. Building an RKE2 cluster
    without egress also requires the node artifacts and chart or image sources
    described in the [air-gapped installation guide](../guides/air-gap.md).

## Download manually

Download the binary and `SHA256SUMS` from the
[GitHub releases page](https://github.com/ryxenix/malmok/releases). For example,
after downloading a Linux amd64 release:

=== "Bash"

    ```bash
    # Replace X.Y.Z with the release version
    MALMOK_BIN=./malmok_vX.Y.Z_linux_amd64
    chmod +x "$MALMOK_BIN"
    sudo install "$MALMOK_BIN" /usr/local/bin/malmok
    malmok --version
    ```

=== "Fish"

    ```fish
    # Replace X.Y.Z with the release version
    set MALMOK_BIN ./malmok_vX.Y.Z_linux_amd64
    chmod +x "$MALMOK_BIN"
    sudo install "$MALMOK_BIN" /usr/local/bin/malmok
    malmok --version
    ```

## Build from source

Go 1.25.8 or newer is required:

```bash
git clone https://github.com/ryxenix/malmok
cd malmok
go build -o bin/malmok ./cmd/malmok
```

`go install github.com/ryxenix/malmok/cmd/malmok@latest` also works, but the
result reports `dev` from `malmok --version`. Use a release binary when exact
version identification matters.

