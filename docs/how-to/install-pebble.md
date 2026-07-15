(how_to_install_pebble)=
# How to install Pebble

To install the latest version of Pebble, choose any of the following methods:

- {ref}`install_pebble_binary`
- {ref}`install_pebble_snap`
- {ref}`install_pebble_from_source`


(install_pebble_binary)=
## Install the binary

To install the binary for the latest version of Pebble:

```{include} /reuse/install.md
   :start-after: Start: Install Pebble binary
   :end-before: End: Install Pebble binary
```


(install_pebble_snap)=
## Install the snap

To install the latest version of Pebble from the Snap Store:

```
sudo snap install pebble --classic
```

For information about snaps, see the [snap documentation](https://snapcraft.io/docs).


(install_pebble_from_source)=
## Install from source

To install the latest version of Pebble from source:

1. Follow the [Go installation documentation](https://go.dev/doc/install) to download and install Go.
2. After installing, add the `$GOBIN` directory to your `$PATH` so you can use the installed tools. For more information, see the [Go environment documentation](https://go.dev/doc/install/source#environment).
3. Run `go install github.com/canonical/pebble/cmd/pebble@latest` to build and install Pebble.


## Verify the Pebble installation

```{include} /reuse/verify.md
   :start-after: Start: Verify the Pebble installation
   :end-before: End: Verify the Pebble installation
```

Pebble is invoked using `pebble <command>`. For more information, see {ref}`reference_pebble_help_command`.

(run_pebble_as_systemd_user_service)=
## Run as a systemd user service

Pebble can run as a systemd user service, which provides automatic startup, restart on failure, and watchdog monitoring.

### Install the service unit

Copy the provided systemd user unit file to your user systemd directory:

```bash
mkdir -p ~/.config/systemd/user/
cp systemd/user/pebble.service ~/.config/systemd/user/
systemctl --user daemon-reload
```

### Enable and start the service

```bash
systemctl --user enable --now pebble
```

### Check the service status

```bash
systemctl --user status pebble
```

### Configure the service

To override the default Pebble directory or enable HTTP/HTTPS access, create an override file:

```bash
systemctl --user edit pebble
```

For example, to change the Pebble directory:

```ini
[Service]
Environment=PEBBLE=/path/to/pebble-dir
```

To enable the HTTP API:

```ini
[Service]
Environment=PEBBLE_HTTP=:4000
ExecStart=/usr/bin/pebble run --create-dirs --http ${PEBBLE_HTTP}
```

### Stop or disable the service

```bash
systemctl --user stop pebble
systemctl --user disable pebble
```

The service unit includes sandboxing directives that restrict Pebble to minimal permissions: read-only access to most of the home directory, no write access to system directories, no new privileges, and network access limited to Unix sockets only. If you enable HTTP/HTTPS API access, you must comment out the `RestrictAddressFamilies=AF_UNIX` line in the override file and add `AF_INET` (and `AF_INET6` for IPv6).
