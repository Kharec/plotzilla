# webkit2_41: Arch and recent distros only ship WebKitGTK 4.1.
TAGS := desktop,production,webkit2_41

PREFIX ?= /usr/local
DESTDIR ?=

.PHONY: build frontend dev test screenshots install uninstall clean

build: frontend
	go build -tags $(TAGS) -ldflags "-s -w" -o build/bin/plotzilla .

frontend:
	cd frontend && npm install && npm run build

# Live reload (requires the Wails CLI: go install github.com/wailsapp/wails/v2/cmd/wails@latest)
dev:
	wails dev -tags webkit2_41

test:
	go test ./internal/...

# README images (screenshots/), from the sample script in tools/screenshots.
# Needs Firefox, and pdftoppm (poppler) for pdf-output.png.
screenshots: frontend
	cd tools/screenshots && npm install --no-fund
	go run ./tools/screenshots $(SCREENSHOTS_FLAGS)

# Binary, launcher, icon and the .fountain / .fdx MIME types. The last three
# lines refresh the desktop caches; they are skipped when staging (DESTDIR).
install:
	install -Dm755 build/bin/plotzilla $(DESTDIR)$(PREFIX)/bin/plotzilla
	install -Dm644 desktop/plotzilla.desktop $(DESTDIR)$(PREFIX)/share/applications/plotzilla.desktop
	install -Dm644 desktop/plotzilla.svg $(DESTDIR)$(PREFIX)/share/icons/hicolor/scalable/apps/plotzilla.svg
	install -Dm644 desktop/plotzilla-mime.xml $(DESTDIR)$(PREFIX)/share/mime/packages/plotzilla.xml
	[ -n "$(DESTDIR)" ] || update-mime-database $(PREFIX)/share/mime || true
	[ -n "$(DESTDIR)" ] || update-desktop-database -q $(PREFIX)/share/applications || true
	[ -n "$(DESTDIR)" ] || gtk-update-icon-cache -qtf $(PREFIX)/share/icons/hicolor || true

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/plotzilla \
		$(DESTDIR)$(PREFIX)/share/applications/plotzilla.desktop \
		$(DESTDIR)$(PREFIX)/share/icons/hicolor/scalable/apps/plotzilla.svg \
		$(DESTDIR)$(PREFIX)/share/mime/packages/plotzilla.xml
	[ -n "$(DESTDIR)" ] || update-mime-database $(PREFIX)/share/mime || true

clean:
	rm -rf build/bin frontend/dist/assets frontend/dist/index.html
