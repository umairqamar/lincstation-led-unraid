# Changelog

Versions are dates (`YYYY.MM.DD`, with a `.N` suffix for a second release on the same day).

## 2026.10.23

First public release.

- Settings page under Settings > Utilities > LincStation LED with an effect picker (steady, breathing, colour fade, off).
- Hexagon colour picker, preset colours, hex code entry and brightness slider.
- Direct control of the LED controller over I2C. No background service and no network port.
- "Match screen colours" option (gamma correction) so pastel colours look right on the LEDs.
- Colour and effect are saved on the flash drive and restored at boot.
- Theme-aware plugin icon (dark and light Unraid themes).
- Optional HTTP API mode (`lincstation-led -port 50000`) that matches the vendor service, for Home Assistant and scripts.
