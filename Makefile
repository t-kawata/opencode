build-kawata:
	rm ./dist/oc-*
	./build
install:
	cp ./dist/oc-* ~/.local/bin/oc
build-install:
	rm ./dist/oc-*
	./build
	cp ./dist/oc-* ~/.local/bin/oc