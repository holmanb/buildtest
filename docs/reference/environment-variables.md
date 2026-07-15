# Pebble Environment Variables

## PEBBLE

Pebble's configuration directory. Defaults to `/var/lib/pebble/default` if not specified.

The `$PEBBLE` directory must contain a `layers/` subdirectory that holds a stack of configuration files. See [general model](../explanation/general-model) and [How to use layers](../how-to/use-layers) for more information.

## PEBBLE_BASEURL

The base URL where the Pebble daemon is expected to be. If set, the client connects over TCP (HTTP or HTTPS) instead of a Unix socket. For example, set to `http://localhost:4000` to connect over plain HTTP, or `https://localhost:8443` to connect over HTTPS with TLS.

If not set, the client connects over the Unix socket at `$PEBBLE_SOCKET` (or the default socket path).

This environment variable is useful when the Pebble daemon is running on a remote host or in a container, and you need the CLI to connect over the network instead of a local socket.

## PEBBLE_COPY_ONCE

To initialize the `$PEBBLE` directory with the contents of another, in a one-time copy, set the `PEBBLE_COPY_ONCE` environment variable to the source directory.

This will only copy the contents if the target directory, `$PEBBLE`, is empty.

## PEBBLE_DEBUG

If set to "1", debug logs will be printed to `stderr`.

## PEBBLE_HTTP

The address for the plain HTTP API server, in `"<address>:port"` format (for example, `:4000`, `192.0.2.0:4000`, `[2001:db8::1]:4000`). If set, the Pebble daemon starts an HTTP API listener on this address in addition to the Unix socket. If not set, the HTTP API server is not started.

For `pebble run`, either `PEBBLE_HTTP=:4000` or the `--http` flag starts the HTTP listener, with the command line flag overriding the environment variable.

## PEBBLE_HTTPS

The address for the HTTPS API server, in `"<address>:port"` format (for example, `:8443`, `192.0.2.0:8443`, `[2001:db8::1]:8443`). If set, the Pebble daemon starts an HTTPS API listener on this address with TLS, in addition to the Unix socket. If not set, the HTTPS API server is not started.

For `pebble run`, either `PEBBLE_HTTPS=:8443` or the `--https` flag starts the HTTPS listener, with the command line flag overriding the environment variable.

## PEBBLE_PERSIST

If set to "never", Pebble will only keep the state in memory without persisting it to a file. If not set, or set any value other than "never", Pebble will persist its state to file `$PEBBLE/.pebble.state` (the default behaviour).

## PEBBLE_SOCKET

Pebble socket path. Defaults to `$PEBBLE/.pebble.socket` if not specified, or `/var/lib/pebble/default/.pebble.socket` if `PEBBLE` is not set.

## PEBBLE_VERBOSE

If set to "1", the Pebble daemon writes service logs to `stdout`.

For `pebble run`, either `PEBBLE_VERBOSE=1` or the `--verbose` flag turns on verbose logging, with the command line flag overriding the environment variable.

For `pebble enter exec`, the `--verbose` flag is currently disallowed. However, `pebble enter` (including `pebble enter exec`) still respects the `PEBBLE_VERBOSE=1` environment variable: the user should know how their applications behave, and that they're okay to use with verbose logging turned on.

## XDG_CONFIG_HOME

The [XDG configuration directory](https://specifications.freedesktop.org/basedir/latest/#basics). Certain Pebble CLI commands create or use data files in `$XDG_CONFIG_HOME/pebble`. Defaults to `$HOME/.config` if not specified.
