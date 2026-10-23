# Contributing

Bug reports, fixes and support for other LincStation models are welcome.

## Before you open a pull request

```
make lint test
```

This checks the shell script, PHP, JavaScript and Go formatting, then runs `go vet` and the Go tests. The tests drive the real command line code with register writes logged to a file, so they need no hardware.

## Layout

| Path | What it is |
| --- | --- |
| `go/` | The `lincstation-led` binary (standard library only, no dependencies) |
| `plugin/src/` | Files installed on the server, laid out as they appear on the Unraid root filesystem |
| `scripts/build.py` | Builds the `.txz` package and the `.plg` installer |
| `docs/` | Protocol notes and screenshots |

## Reporting hardware problems

Please include the output of `lincstation-led -version`, your Unraid version, `ls /dev/i2c-*`, and the last lines of `/var/log/lincstation-led.log`.

## Releasing (maintainers)

Update `CHANGELOG.md`, then push a tag:

```
git tag v2026.10.24 && git push origin v2026.10.24
```

The release workflow builds the package and publishes `lincstation-led.plg`, the `.txz` and `SHA256SUMS`. The installer URL in the README always points at the latest release.
