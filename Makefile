# Machine-specific overrides (KEY, HOST, CONFIG) go in local.mk, which is not committed.
-include local.mk

HOST ?= 192.168.15.244
KEY  ?= $(HOME)/.ssh/id_ed25519
SSH   = ssh -i "$(KEY)" -o BatchMode=yes -o ConnectTimeout=5 root@$(HOST)
SCP   = scp -i "$(KEY)" -o BatchMode=yes -O
REMOTE = /mnt/us/extensions/amber
CONFIG ?= amber.json

BIN = build/amber

.PHONY: build test preview demo deploy push-config pull-config start stop autostart-on autostart-off log clean

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags="-s -w" -o $(BIN) .

test:
	CGO_ENABLED=0 go test ./...

# Render one frame on the Mac: live data from $(CONFIG), or made-up data.
preview:
	CGO_ENABLED=0 go run . -config $(CONFIG) -png build/preview.png
demo:
	CGO_ENABLED=0 go run . -config $(CONFIG) -demo -png build/preview.png

# Installs the binary and the KUAL extension. The config on the device is left
# alone if it exists, because the web UI may have changed it; see push-config.
deploy: build
	$(SSH) "mkdir -p $(REMOTE)/bin"
	$(SCP) extension/amber/config.xml extension/amber/menu.json root@$(HOST):$(REMOTE)/
	$(SSH) "$(REMOTE)/bin/stop.sh 2>/dev/null; sleep 1; rm -f $(REMOTE)/bin/amber"
	$(SCP) extension/amber/bin/* $(BIN) root@$(HOST):$(REMOTE)/bin/
	$(SSH) "chmod 755 $(REMOTE)/bin/*; test -f $(REMOTE)/amber.json" || $(MAKE) push-config

push-config:
	CGO_ENABLED=0 go run . -config $(CONFIG) -check
	$(SCP) $(CONFIG) root@$(HOST):$(REMOTE)/amber.json
	$(SSH) "chmod 600 $(REMOTE)/amber.json"

pull-config:
	$(SCP) root@$(HOST):$(REMOTE)/amber.json $(CONFIG)

start:
	$(SSH) "$(REMOTE)/bin/start.sh"

stop:
	$(SSH) "$(REMOTE)/bin/stop.sh"

autostart-on:
	$(SSH) "$(REMOTE)/bin/autostart.sh on"

autostart-off:
	$(SSH) "$(REMOTE)/bin/autostart.sh off"

log:
	$(SSH) "tail -n 60 $(REMOTE)/amber.log"

clean:
	rm -rf build
