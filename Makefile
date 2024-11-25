.POSIX:
.SUFFIXES:

SERVICE = premiumizearrd
GO = go
RM = rm
GOFLAGS =
PREFIX = /usr/local
BUILDDIR = build

all: clean build

web: deps/web build/web
app: deps/app build/app

deps/web:
	cd web && npm i

deps/app:
	$(GO) mod download

build: web app
	
build/app: deps/app
	mkdir -p $(BUILDDIR)
	$(GO) build -o $(BUILDDIR)/$(SERVICE) ./cmd/$(SERVICE) $(GOFLAGS)
	cp init/* $(BUILDDIR)/

build/web:
	mkdir -p $(BUILDDIR)
	cd web && npm run build
	mkdir -p $(BUILDDIR)/static/ && cp -r web/dist/* $(BUILDDIR)/static/

clean:
	$(RM) -rf build

run:
	cd $(BUILDDIR) && ./$(SERVICE)
