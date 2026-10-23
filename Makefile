VERSION ?= $(shell date +%Y.%m.%d)
REPO    ?= umairqamar/lincstation-led-unraid

.PHONY: all test lint build clean

all: lint test build

test:
	cd go && go vet ./... && go test ./...

lint:
	bash -n plugin/src/etc/rc.d/rc.lincstation-led
	php -l plugin/src/usr/local/emhttp/plugins/lincstation-led/include/exec.php
	node --check plugin/src/usr/local/emhttp/plugins/lincstation-led/js/lincstation-led.js
	cd go && test -z "$$(gofmt -l .)"

build:
	python3 scripts/build.py --version $(VERSION) --repo $(REPO)

clean:
	rm -rf dist go/lincstation-led
