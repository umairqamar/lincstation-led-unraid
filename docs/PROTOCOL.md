# LincStation N1 LED protocol

These notes describe how the LED controller in the LincStation N1 is driven. They were worked out by observing the behaviour of the original vendor service (which shells out to `i2cset`) and checking each register against it. No vendor code is included in this repository.

## Hardware access

- I2C bus 0, device address `0x26` (`/dev/i2c-0`, needs the `i2c-dev` kernel module).
- Single register writes (SMBus "write byte data").
- The vendor service runs `i2cset -y 0 0x26 <register> <value>`. This project does the same thing with the `I2C_SLAVE` and `I2C_SMBUS` ioctls, so `i2c-tools` is not needed.

## RGB light bar

| Register | Meaning |
| --- | --- |
| `0x90` | Mode: `0` steady, `1` breathing, `2` colour loop |
| `0x91` | Brightness, 1 to 255 |
| `0x92` `0x93` `0x94` | Red, green, blue (steady and breathing) |
| `0x95` `0x96` `0x97` | Red, green, blue of the first colour (loop) |
| `0x98` `0x99` `0x9A` | Red, green, blue of the second colour (loop) |

Off is `0x90 = 0` and `0x91 = 0`. Breathing mode ignores the brightness register.

## Status LEDs

Each status LED has a white and a yellow element. The controller has "set bits" and "clear bits" registers:

| LED | Set / clear register | White bit | Yellow bit | Blink registers |
| --- | --- | --- | --- | --- |
| power | `0xA0` / `0xB0` | `0x01` | `0x02` | `0x50` `0x51` |
| network | `0xA0` / `0xB0` | `0x40` | `0x80` | `0x56` `0x57` |
| hhd0 | `0xA0` / `0xB0` | `0x04` | `0x08` | `0x52` `0x53` |
| hhd1 | `0xA0` / `0xB0` | `0x10` | `0x20` | `0x54` `0x55` |
| nvme0 | `0xA1` / `0xB1` | `0x01` | `0x02` | `0x58` `0x59` |
| nvme1 | `0xA1` / `0xB1` | `0x04` | `0x08` | `0x5A` `0x5B` |
| nvme2 | `0xA1` / `0xB1` | `0x10` | `0x20` | `0x5C` `0x5D` |
| nvme3 | `0xA1` / `0xB1` | `0x40` | `0x80` | `0x5E` `0x5F` |

States are written as: normal = set white, clear yellow; warning = clear white, set yellow; alert = set both; off = clear both. The vendor service writes `0` to each LED's blink registers at start.

## Colour correction

Screen colour values are gamma encoded, but the LED brightness is linear PWM. Sending `#87CEEB` unchanged looks washed out. With "Match screen colours" on, each channel is sent as `round(255 * (v / 255) ^ 2.2)` (never zero for a non-zero input). The value you picked is what is saved and shown; only the register values are corrected.

## HTTP API mode (optional)

`lincstation-led -port 50000` runs a server that matches the vendor service, so existing Home Assistant integrations keep working. The Unraid plugin does **not** start it.

| Request | Fields | Notes |
| --- | --- | --- |
| `POST /rgb/mode` | `mode` (`off`, `normal`, `breath`, `loop`), `light` (1 to 255), `color0`, `color1` as `#RRGGBB` | Form encoded. `normal` needs `color0` and `light`, `breath` needs `color0`, `loop` needs both colours and `light`. |
| `GET /rgb/mode` | | `{"Mode","Light","Color0","Color1"}` |
| `POST /led/switch` | `enabled` (boolean) | Master switch for the status LEDs |
| `GET /led/switch` | | `{"enabled","message"}` |
| `POST /led/opt` | `targetLed`, `state` (`normal`, `warning`, `alert`, `off`) | Unknown LED gives HTTP 500 `{"Message":"Target not exist"}` |
| `GET /health` | | Liveness check |

There is no authentication, so bind to localhost (`-bind 127.0.0.1`) or a trusted network only.
