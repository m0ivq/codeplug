SHELL = /bin/sh

.PHONY: default linux windows clean clobber upload install tag

EDITCP_SOURCES = *.go
UI_SOURCES = ../ui/*.go
CODEPLUG_SOURCES = ../codeplug/*.go
SOURCES = $(EDITCP_SOURCES) $(UI_SOURCES) $(CODEPLUG_SOURCES)
VERSION = $(shell sed -n '/version =/{s/^[^"]*"//;s/".*//p;q}' <version.go)

default: linux

linux: deploy/linux/editcp deploy/linux/editcp.sh deploy/linux/install

deploy/linux/editcp: $(SOURCES)
	qtdeploy -docker build

.PHONY: deploy/linux/editcp.sh	# Force, in case it's overwritten by install
deploy/linux/editcp.sh: editcp.sh
	cp editcp.sh deploy/linux/editcp.sh

deploy/linux/install: install.sh deploy/linux/editcp
	cp install.sh deploy/linux/install

editcp-$(VERSION).tar.xz: deploy/linux/editcp.sh
	rm -rf editcp-$(VERSION)
	mkdir -p editcp-$(VERSION)
	cp -al deploy/linux/* editcp-$(VERSION)
	tar cJf editcp-$(VERSION).tar.xz editcp-$(VERSION)
	rm -rf editcp-$(VERSION)

install: linux
	cd deploy/linux && ./install .

windows: editcp-$(VERSION).exe

editcp-$(VERSION).exe: deploy/win32/editcp.exe deploy/win64/editcp.exe editcp.nsi
	makensis -DVERSION=$(VERSION) editcp.nsi

deploy/win32/editcp.exe: $(SOURCES)
	qtdeploy -docker build windows_32_static
	mkdir -p deploy/win32
	cp deploy/windows/editcp.exe deploy/win32

deploy/win64/editcp.exe: $(SOURCES)
	qtdeploy -docker build windows_64_static
	mkdir -p deploy/win64
	cp deploy/windows/editcp.exe deploy/win64

clean:

clobber: clean
	rm -rf deploy

# The targets below are probably only useful for me. -Dale Farnsworth

upload: editcp-$(VERSION).tar.xz editcp-$(VERSION).exe
	rsync editcp-$(VERSION).tar.xz farnsworth.org:
	rsync editcp-$(VERSION).exe farnsworth.org:

tag:
	git tag -s -m "editcp v$(VERSION)" v$(VERSION)
