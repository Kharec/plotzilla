# webkit2_41: Arch and recent distros only ship WebKitGTK 4.1.
TAGS := desktop,production,webkit2_41

.PHONY: build frontend dev test screenshots clean

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

clean:
	rm -rf build/bin frontend/dist/assets frontend/dist/index.html
